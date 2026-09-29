package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// ==========================================
// SEGURIDAD & HASHEO DE CONTRASEÑAS (BCRYPT)
// ==========================================

func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(bytes), err
}

func checkPasswordHash(password, hash string) bool {
	if hash == "" || password == "" {
		return false
	}
	if strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$") {
		err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
		return err == nil
	}
	// Compatibilidad para migración automática de contraseñas previas en texto plano
	return password == hash
}

// ==========================================
// GESTIÓN DE SESIONES & TOKENS SEGUROS (HMAC-SHA256)
// ==========================================

var sessionSecret []byte

func initSessionSecret() {
	sec := os.Getenv("SESSION_SECRET")
	if sec != "" {
		sessionSecret = []byte(sec)
	} else {
		b := make([]byte, 32)
		_, err := cryptorand.Read(b)
		if err != nil {
			sessionSecret = []byte("kido_garage_secret_key_production_2026_salt_32bytes")
		} else {
			sessionSecret = b
		}
	}
}

func generateSessionToken(u User) string {
	payload := fmt.Sprintf("%d:%s:%s:%d", u.ID, u.Email, u.Role, time.Now().Unix())
	h := hmac.New(sha256.New, sessionSecret)
	h.Write([]byte(payload))
	signature := hex.EncodeToString(h.Sum(nil))
	token := base64.URLEncoding.EncodeToString([]byte(payload + "|" + signature))
	return token
}

func validateSessionToken(tokenStr string) (*User, error) {
	if tokenStr == "" {
		return nil, fmt.Errorf("token vacío")
	}
	raw, err := base64.URLEncoding.DecodeString(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("token inválido")
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 2 {
		return nil, fmt.Errorf("formato de token incorrecto")
	}
	payload := parts[0]
	signature := parts[1]

	h := hmac.New(sha256.New, sessionSecret)
	h.Write([]byte(payload))
	expectedSig := hex.EncodeToString(h.Sum(nil))
	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return nil, fmt.Errorf("firma de token inválida")
	}

	payloadParts := strings.Split(payload, ":")
	if len(payloadParts) < 4 {
		return nil, fmt.Errorf("datos de sesión corruptos")
	}
	userID, _ := strconv.ParseInt(payloadParts[0], 10, 64)
	email := payloadParts[1]
	role := payloadParts[2]
	issuedAt, _ := strconv.ParseInt(payloadParts[3], 10, 64)

	// Expiración a los 7 días
	if time.Now().Unix()-issuedAt > 7*24*3600 {
		return nil, fmt.Errorf("sesión expirada")
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, u := range store.users {
		if u.ID == userID && strings.EqualFold(u.Email, email) {
			userCopy := u
			userCopy.Password = ""
			return &userCopy, nil
		}
	}

	return &User{
		ID:    userID,
		Email: email,
		Role:  role,
	}, nil
}

func getAuthenticatedUser(r *http.Request) (*User, error) {
	// 1. Intentar desde Cookie 'kido_session'
	if cookie, err := r.Cookie("kido_session"); err == nil && cookie.Value != "" {
		if user, err := validateSessionToken(cookie.Value); err == nil {
			return user, nil
		}
	}

	// 2. Intentar desde Header 'Authorization: Bearer <token>'
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		return validateSessionToken(token)
	}

	// 3. Intentar desde Query param 'token'
	if qToken := r.URL.Query().Get("token"); qToken != "" {
		return validateSessionToken(qToken)
	}

	return nil, fmt.Errorf("no autenticado")
}

// requireAdmin protege los endpoints administrativos
func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		user, err := getAuthenticatedUser(r)
		if err != nil || user == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Autenticación requerida. Inicia sesión como administrador.",
			})
			return
		}

		if !strings.EqualFold(user.Role, "ADMIN") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Acceso denegado: Se requieren permisos de Administrador.",
			})
			return
		}

		next(w, r)
	}
}

// ==========================================
// RATE LIMITER (PROTECCIÓN CONTRA FUERZA BRUTA & DDOS)
// ==========================================

type RateLimiter struct {
	mu           sync.Mutex
	loginFails   map[string][]time.Time
	generalReqs  map[string][]time.Time
	maxFails     int           // Max fallos permitidos antes de bloqueo
	failWindow   time.Duration // Ventana de tiempo para fallos (5 min)
	maxReqPerMin int           // Max peticiones por minuto general
}

var globalLimiter = &RateLimiter{
	loginFails:   make(map[string][]time.Time),
	generalReqs:  make(map[string][]time.Time),
	maxFails:     5,
	failWindow:   5 * time.Minute,
	maxReqPerMin: 180,
}

func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return strings.TrimSpace(xrip)
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func (rl *RateLimiter) RecordFailedLogin(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	rl.loginFails[ip] = append(rl.loginFails[ip], now)
}

func (rl *RateLimiter) ResetFailedLogins(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.loginFails, ip)
}

func (rl *RateLimiter) IsLoginBlocked(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	var valid []time.Time
	for _, t := range rl.loginFails[ip] {
		if now.Sub(t) <= rl.failWindow {
			valid = append(valid, t)
		}
	}
	rl.loginFails[ip] = valid
	return len(valid) >= rl.maxFails
}

func (rl *RateLimiter) AllowGeneral(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	var valid []time.Time
	for _, t := range rl.generalReqs[ip] {
		if now.Sub(t) <= time.Minute {
			valid = append(valid, t)
		}
	}
	if len(valid) >= rl.maxReqPerMin {
		rl.generalReqs[ip] = valid
		return false
	}
	valid = append(valid, now)
	rl.generalReqs[ip] = valid
	return true
}

func rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)

		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !globalLimiter.AllowGeneral(ip) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": false,
					"error":   "Demasiadas peticiones. Por favor espera un momento.",
				})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// ==========================================
// CORS & CABECERAS DE SEGURIDAD
// ==========================================

func corsMiddleware(next http.Handler) http.Handler {
	allowedOriginEnv := os.Getenv("ALLOWED_ORIGIN")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if allowedOriginEnv != "" && allowedOriginEnv != "*" {
				if origin == allowedOriginEnv {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				}
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}

		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ==========================================
// MODELOS DE DATOS
// ==========================================

type User struct {
	ID                   int64  `json:"id"`
	Email                string `json:"email"` // Nombre de usuario
	Password             string `json:"password,omitempty"`
	Role                 string `json:"role"` // "ADMIN" o "CLIENT"
	GoogleID             string `json:"google_id,omitempty"`
	FirstName            string `json:"first_name"`
	LastName             string `json:"last_name"`
	Phone                string `json:"phone"`
	Locality             string `json:"locality"`
	Street               string `json:"street"`
	StreetNumber         string `json:"street_number"`
	AvatarURL            string `json:"avatar_url"`
	ConsecutiveMonths    int    `json:"consecutive_months"`      // Meses seguidos con compra
	IsFrequentCustomer   bool   `json:"is_frequent_customer"`    // true si > 3 meses
	FrequentPoints       int    `json:"frequent_points"`         // 1 pt por cada mes extra después de los 3 meses
	TotalPurchasesCount  int    `json:"total_purchases_count"`   // 1 pt por cada compra
	HasClaimedFreeRaffle bool   `json:"has_claimed_free_raffle"` // 1 ticket gratis por sorteo
}

type ProductVariant struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	SKU       string `json:"sku"`
	Price     string `json:"price"` // USD
	Available bool   `json:"available"`
}

type ProductImage struct {
	ID       int64  `json:"id"`
	Position int    `json:"position"`
	Src      string `json:"src"`
}

type Brand struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	Carrusel bool   `json:"carrusel"`
	Imagen   string `json:"imagen"`
}

type ProductTypeModel struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	IsActive bool   `json:"is_active"`
}

type Product struct {
	ID             int64            `json:"id"`
	InternalCode   string           `json:"internal_code,omitempty"`
	Title          string           `json:"title"`
	Handle         string           `json:"handle"`
	BodyHTML       string           `json:"body_html"`
	Vendor         string           `json:"vendor"`
	BrandID        *int64           `json:"brand_id,omitempty"`
	ProductType    string           `json:"product_type"` // "Autito", "Remera", "Sticker"
	ProductTypeID  *int64           `json:"product_type_id,omitempty"`
	Scale          string           `json:"scale,omitempty"`        // Requerido si Autito (1:64, 1:43, 1:18)
	ApparelSize    string           `json:"apparel_size,omitempty"` // Requerido si Remera (S, M, L, XL, XXL)
	Tags           []string         `json:"tags"`
	Variants       []ProductVariant `json:"variants"`
	Images         []ProductImage   `json:"images"`
	GalleryImages  []string         `json:"gallery_images"`
	ShortVideoURL  string           `json:"short_video_url,omitempty"` // Video muy corto del artículo
	PriceARS       int              `json:"price_ars"`
	PriceUSD       string           `json:"price_usd"`
	Status         string           `json:"status"` // "STOCK", "PRE_VENTA", "AGOTADO"
	StockQuantity  int              `json:"stock_quantity"`
	IsActive       bool             `json:"is_active"` // Activo Sí/No
	HasChaseChance bool             `json:"has_chase_chance"`
	InSlider       bool             `json:"in_slider"` // Mostrar en Slider / Carrusel
}

type SliderConfig struct {
	MaxItems int  `json:"max_items"` // Cantidad máxima de artículos a mostrar en el slider
	Active   bool `json:"active"`    // Si el slider está activo o no
}

type BulkImportProductItem struct {
	InternalCode   string `json:"codigo_interno"`
	Title          string `json:"titulo"`
	ProductType    string `json:"tipo_articulo"`
	Vendor         string `json:"marca"`
	Scale          string `json:"escala"`
	ApparelSize    string `json:"talle"`
	PriceARS       int    `json:"precio_ars"`
	PriceUSD       string `json:"precio_usd"`
	StockQuantity  int    `json:"stock_cantidad"`
	Status         string `json:"estado"`
	Activo         string `json:"activo"`
	HasChaseChance string `json:"chance_chase"`
	InSlider       string `json:"en_slider,omitempty"`
	GalleryImages  string `json:"imagenes_galeria"`
	ShortVideoURL  string `json:"video_url"`
	Description    string `json:"descripcion"`
}

type ProductCatalog struct {
	Products []Product `json:"products"`
}

type PartialPayment struct {
	ID            int64     `json:"id"`
	OrderID       int64     `json:"order_id"`
	ProductID     int64     `json:"product_id"`
	AmountARS     int       `json:"amount_ars"`
	PaymentMethod string    `json:"payment_method"`
	MercadoPagoID string    `json:"mercadopago_id"`
	PaymentStatus string    `json:"payment_status"` // "approved"
	IsDownPayment bool      `json:"is_downpayment"` // Seña/reserva
	CreatedAt     time.Time `json:"created_at"`
}

type OrderItem struct {
	ProductID    int64  `json:"product_id"`
	ProductTitle string `json:"product_title"`
	Quantity     int    `json:"quantity"`
	UnitPriceARS int    `json:"unit_price_ars"`
	SubtotalARS  int    `json:"subtotal_ars"`
	ProductType  string `json:"product_type"`
	ScaleOrSize  string `json:"scale_or_size"`
}

type Order struct {
	ID                  int64            `json:"id"`
	OrderNumber         string           `json:"order_number"`
	CustomerID          int64            `json:"customer_id"`
	CustomerEmail       string           `json:"customer_email"`
	CustomerName        string           `json:"customer_name"`
	TotalARS            int              `json:"total_ars"`
	TotalPaidARS        int              `json:"total_paid_ars"`
	RemainingBalanceARS int              `json:"remaining_balance_ars"`
	IsFullyPaid         bool             `json:"is_fully_paid"`
	DeliveryStatus      string           `json:"delivery_status"` // "BLOQUEADO_POR_SALDO", "LISTO_PARA_DESPACHAR", "EN_CAMINO", "ENTREGADO"
	OrderType           string           `json:"order_type"`      // "VENTA_DIRECTA" o "PRE_VENTA_CON_SEÑA"
	ShippingMethod      string           `json:"shipping_method"` // "Correo Argentino - Envío a Domicilio", "Correo Argentino - Sucursal", "Andreani Express", "Punto de Retiro KIDO"
	ShippingCostARS     int              `json:"shipping_cost_ars"`
	ShippingPostalCode  string           `json:"shipping_postal_code"`
	ShippingAddress     string           `json:"shipping_address,omitempty"`
	TrackingNumber      string           `json:"tracking_number"`
	TrackingCarrier     string           `json:"tracking_carrier"` // "Correo Argentino", "Andreani", "Otro"
	Items               []OrderItem      `json:"items"`
	Payments            []PartialPayment `json:"payments"`
	CreatedAt           time.Time        `json:"created_at"`
}

type ShippingOption struct {
	ID              string `json:"id"`
	Carrier         string `json:"carrier"` // "Correo Argentino", "Andreani", "KIDO Garage"
	Name            string `json:"name"`
	EstimatedDays   string `json:"estimated_days"`
	CostARS         int    `json:"cost_ars"`
	OriginalCostARS int    `json:"original_cost_ars"`
	IsFree          bool   `json:"is_free"`
	Badge           string `json:"badge"`
}

type RaffleTicket struct {
	Number        int       `json:"number"`
	CustomerID    int64     `json:"customer_id"`
	CustomerEmail string    `json:"customer_email"`
	CustomerName  string    `json:"customer_name"`
	IsFreeTicket  bool      `json:"is_free_ticket"`
	MercadoPagoID string    `json:"mercadopago_id"`
	PurchasedAt   time.Time `json:"purchased_at"`
}

type Raffle struct {
	ID               int64                `json:"id"`
	RaffleNumber     string               `json:"raffle_number"` // "RIFA-#01-CHASE-R34"
	Title            string               `json:"title"`
	PrizeDescription string               `json:"prize_description"`
	PrizeImages      []string             `json:"prize_images"`
	StartDatetime    string               `json:"start_datetime"`
	EndDatetime      string               `json:"end_datetime"`
	DrawDatetime     string               `json:"draw_datetime"`
	MinNumber        int                  `json:"min_number"` // ej: 0
	MaxNumber        int                  `json:"max_number"` // ej: 99
	TicketPriceARS   int                  `json:"ticket_price_ars"`
	Status           string               `json:"status"`  // "ACTIVA", "FINALIZADA", "SORTEADA"
	Tickets          map[int]RaffleTicket `json:"tickets"` // number -> ticket
	WinnerNumber     *int                 `json:"winner_number,omitempty"`
}

type Expense struct {
	ID            int       `json:"id"`
	Category      string    `json:"category"`
	Concept       string    `json:"concept"`
	Supplier      string    `json:"supplier"`
	AmountARS     float64   `json:"amount_ars"`
	AmountUSD     float64   `json:"amount_usd"`
	InvoiceNumber string    `json:"invoice_number"`
	Date          string    `json:"date"`
	CreatedAt     time.Time `json:"created_at"`
}

type PurchaseOrder struct {
	ID           int       `json:"id"`
	PONumber     string    `json:"po_number"`
	SupplierName string    `json:"supplier_name"`
	Status       string    `json:"status"` // BORRADOR, EMITIDA, EN_TRANSITO, RECIBIDA
	OrderDate    string    `json:"order_date"`
	ExpectedETA  string    `json:"expected_eta"`
	TotalUSD     float64   `json:"total_usd"`
	TrackingNum  string    `json:"tracking_number"`
	CreatedAt    time.Time `json:"created_at"`
}

// ==========================================
// MEMORIA CENTRAL (THREAD SAFE)
// ==========================================

type StoreData struct {
	mu             sync.RWMutex
	users          []User
	products       []Product
	orders         []Order
	raffles        []Raffle
	expenses       []Expense
	purchaseOrders []PurchaseOrder
	brands         []Brand
	productTypes   []ProductTypeModel
	sliderConfig   SliderConfig
}

var store = &StoreData{
	sliderConfig: SliderConfig{
		MaxItems: 5,
		Active:   true,
	},
	users: []User{
		{
			ID:                  1,
			Email:               "admin@kido.com.ar",
			Password:            "Lujo$2404",
			Role:                "ADMIN",
			FirstName:           "Kido",
			LastName:            "Admin",
			Phone:               "+54 11 5555-0100",
			Locality:            "CABA",
			Street:              "Av. Cabildo",
			StreetNumber:        "2400",
			AvatarURL:           "https://images.unsplash.com/photo-1534528741775-53994a69daeb?auto=format&fit=crop&w=200&q=80",
			ConsecutiveMonths:   0,
			IsFrequentCustomer:  false,
			FrequentPoints:      0,
			TotalPurchasesCount: 0,
		},
	},
	expenses:       []Expense{},
	purchaseOrders: []PurchaseOrder{},
	brands: []Brand{
		{ID: 1, Name: "Kaido House", Code: "KAIDOHOUSE", Carrusel: true, Imagen: ""},
		{ID: 2, Name: "Mini GT", Code: "MINIGT", Carrusel: true, Imagen: ""},
		{ID: 3, Name: "Pop Race", Code: "POPRACE", Carrusel: true, Imagen: ""},
		{ID: 4, Name: "Inno64", Code: "INNO64", Carrusel: true, Imagen: ""},
		{ID: 5, Name: "Tarmac Works", Code: "TARMAC", Carrusel: true, Imagen: ""},
		{ID: 6, Name: "Hot Wheels", Code: "HOTWHEELS", Carrusel: true, Imagen: ""},
		{ID: 7, Name: "Spark", Code: "SPARK", Carrusel: true, Imagen: ""},
		{ID: 8, Name: "Ignition Model", Code: "IGNITION", Carrusel: true, Imagen: ""},
		{ID: 9, Name: "KIDO Apparel", Code: "KIDO_APP", Carrusel: false, Imagen: ""},
		{ID: 10, Name: "KIDO Accessories", Code: "KIDO_ACC", Carrusel: false, Imagen: ""},
	},
	productTypes: []ProductTypeModel{
		{ID: 1, Name: "Autito", Code: "DIECAST", IsActive: true},
		{ID: 2, Name: "Remera", Code: "APPAREL", IsActive: true},
		{ID: 3, Name: "Sticker", Code: "STICKER", IsActive: true},
	},
}

// Inicializar Rifas (vacío para producción limpia)
func initRaffles() {
	store.raffles = []Raffle{}
}

// Inicializar Pedidos (vacío para producción limpia)
func initOrders() {
	store.orders = []Order{}
}

// calculateShippingRates determina la zona y calcula las tarifas de Correo Argentino, Andreani y Retiro Oficial KIDO
func calculateShippingRates(postalCode string, cartTotal int) (string, []ShippingOption) {
	var digits strings.Builder
	for _, ch := range postalCode {
		if ch >= '0' && ch <= '9' {
			digits.WriteRune(ch)
		}
	}
	cpNum, _ := strconv.Atoi(digits.String())

	zone := "Tarifa Estándar Nacional"
	costCorreoSuc := 5500
	costCorreoDom := 7500
	costAndreani := 9200

	switch {
	case cpNum >= 1000 && cpNum <= 1999:
		zone = "CABA y Gran Buenos Aires (AMBA)"
		costCorreoSuc = 3800
		costCorreoDom = 5200
		costAndreani = 6900
	case (cpNum >= 2000 && cpNum <= 3999):
		zone = "Litoral y Centro Este (Santa Fe, Entre Ríos, Corrientes, Misiones)"
		costCorreoSuc = 5400
		costCorreoDom = 7200
		costAndreani = 8900
	case (cpNum >= 5000 && cpNum <= 5999):
		zone = "Región Centro (Córdoba)"
		costCorreoSuc = 5400
		costCorreoDom = 7200
		costAndreani = 8900
	case (cpNum >= 6000 && cpNum <= 7999):
		zone = "Buenos Aires Interior y Región Cuyo (Mendoza, San Juan, San Luis)"
		costCorreoSuc = 5900
		costCorreoDom = 7800
		costAndreani = 9800
	case (cpNum >= 4000 && cpNum <= 4999):
		zone = "Región NOA (Tucumán, Salta, Jujuy, Catamarca, Santiago del Estero)"
		costCorreoSuc = 6500
		costCorreoDom = 8600
		costAndreani = 10500
	case (cpNum >= 8000 && cpNum <= 9999):
		zone = "Región Patagonia (Neuquén, Río Negro, Chubut, Santa Cruz, TDF)"
		costCorreoSuc = 7500
		costCorreoDom = 9900
		costAndreani = 12800
	}

	hasFreeShipping := cartTotal >= 80000

	finalCorreoSuc := costCorreoSuc
	badgeCorreoSuc := "Económico"
	isFreeSuc := false
	if hasFreeShipping {
		finalCorreoSuc = 0
		badgeCorreoSuc = "¡ENVÍO GRATIS!"
		isFreeSuc = true
	}

	finalCorreoDom := costCorreoDom
	badgeCorreoDom := "Recomendado"
	isFreeDom := false
	if hasFreeShipping {
		finalCorreoDom = 0
		badgeCorreoDom = "¡ENVÍO GRATIS!"
		isFreeDom = true
	}

	finalAndreani := costAndreani
	badgeAndreani := "Prioritario 24/48hs"
	if hasFreeShipping {
		finalAndreani = costAndreani / 2
		badgeAndreani = "50% OFF PROMO"
	}

	options := []ShippingOption{
		{
			ID:              "correo_domicilio",
			Carrier:         "Correo Argentino",
			Name:            "Correo Argentino - Envío Clásico a Domicilio",
			EstimatedDays:   "3 a 5 días hábiles",
			CostARS:         finalCorreoDom,
			OriginalCostARS: costCorreoDom,
			IsFree:          isFreeDom,
			Badge:           badgeCorreoDom,
		},
		{
			ID:              "correo_sucursal",
			Carrier:         "Correo Argentino",
			Name:            "Correo Argentino - Retiro en Sucursal más cercana",
			EstimatedDays:   "2 a 4 días hábiles",
			CostARS:         finalCorreoSuc,
			OriginalCostARS: costCorreoSuc,
			IsFree:          isFreeSuc,
			Badge:           badgeCorreoSuc,
		},
		{
			ID:              "andreani_express",
			Carrier:         "Andreani",
			Name:            "Andreani Express - Puerta a Puerta Prioritario",
			EstimatedDays:   "24 a 48 hs hábiles",
			CostARS:         finalAndreani,
			OriginalCostARS: costAndreani,
			IsFree:          false,
			Badge:           badgeAndreani,
		},
		{
			ID:              "retiro_kido",
			Carrier:         "KIDO Garage",
			Name:            "Retiro Oficial en KIDO Garage (Villa Urquiza, CABA)",
			EstimatedDays:   "Inmediato con coordinación previa",
			CostARS:         0,
			OriginalCostARS: 0,
			IsFree:          true,
			Badge:           "PUNTO GRATIS",
		},
	}

	return zone, options
}

// Cargar Catálogo (limpio por defecto en producción a menos que SEED_SAMPLE_DATA=true)
func loadCatalog() {
	if os.Getenv("SEED_SAMPLE_DATA") != "true" {
		store.mu.Lock()
		store.products = []Product{}
		store.mu.Unlock()
		log.Printf("📦 Catálogo inicializado en BLANCO (producción limpia)")
		return
	}

	filePath := filepath.Join(".", "products_sample.json")
	file, err := os.Open(filePath)
	if err != nil {
		filePath = filepath.Join(".", "public", "products_sample.json")
		file, err = os.Open(filePath)
		if err != nil {
			log.Printf("Error cargando productos: %v", err)
			return
		}
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		log.Printf("Error leyendo JSON: %v", err)
		return
	}

	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	var catalog ProductCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		log.Printf("Error deserializando productos: %v", err)
		return
	}

	usdToArsRate := 1350.0

	for i := range catalog.Products {
		p := &catalog.Products[i]
		p.ProductType = "Autito" // Por defecto en diecast
		p.IsActive = true

		scale := "1:64"
		for _, tag := range p.Tags {
			if strings.Contains(tag, "1:18") {
				scale = "1:18"
			} else if strings.Contains(tag, "1:43") {
				scale = "1:43"
			} else if strings.Contains(tag, "1:64") {
				scale = "1:64"
			}
			if strings.Contains(tag, "Chase") || strings.Contains(tag, "CHANCE") {
				p.HasChaseChance = true
			}
		}
		p.Scale = scale

		p.Status = "STOCK"
		for _, tag := range p.Tags {
			if strings.EqualFold(tag, "Pre-Order") || strings.Contains(strings.ToLower(tag), "pre-order") {
				p.Status = "PRE_VENTA"
				break
			}
		}
		if strings.Contains(strings.ToLower(p.Title), "pre-order") {
			p.Status = "PRE_VENTA"
		}

		priceUSDStr := "19.99"
		if len(p.Variants) > 0 && p.Variants[0].Price != "" {
			priceUSDStr = p.Variants[0].Price
		}
		p.PriceUSD = priceUSDStr
		priceUSDFloat, _ := strconv.ParseFloat(priceUSDStr, 64)
		if priceUSDFloat <= 0 {
			priceUSDFloat = 20.0
		}

		calculatedArs := int(priceUSDFloat * usdToArsRate)
		calculatedArs = (calculatedArs / 100) * 100
		p.PriceARS = calculatedArs
		p.StockQuantity = 14
		if p.Status == "PRE_VENTA" {
			p.StockQuantity = 30
		}

		// Galería de imágenes
		for _, img := range p.Images {
			p.GalleryImages = append(p.GalleryImages, img.Src)
		}
		// Video corto referencial
		p.ShortVideoURL = "https://assets.mixkit.co/videos/preview/mixkit-car-wheel-turning-on-the-road-4263-large.mp4"
	}

	// Remeras y Stickers
	apparelAndStickers := []Product{
		{
			ID:            999001,
			Title:         "Remera Oversize JDM Culture Black - KIDO Garage",
			Handle:        "remera-oversize-jdm-culture-black",
			BodyHTML:      "<p>Remera 100% Algodón Peinado 24/1 pesado. Estampa serigráfica de alta durabilidad en espalda y pecho. Corte boxy fit oversize japonés.</p>",
			Vendor:        "KIDO Apparel",
			ProductType:   "Remera",
			ApparelSize:   "L (Oversize)",
			Scale:         "",
			Tags:          []string{"Indumentaria", "Remeras", "JDM", "Streetwear"},
			PriceARS:      14000,
			PriceUSD:      "10.50",
			Status:        "STOCK",
			StockQuantity: 40,
			IsActive:      true,
			GalleryImages: []string{"https://images.unsplash.com/photo-1521572267360-ee0c2909d518?auto=format&fit=crop&w=800&q=80"},
			ShortVideoURL: "",
		},
		{
			ID:            999002,
			Title:         "Pack x10 Stickers Vinilo Holográfico Resistente al Agua",
			Handle:        "pack-10-stickers-vinilo-diecast-jdm",
			BodyHTML:      "<p>Pack de 10 stickers troquelados de vinilo premium con laminado UV resistente a la intemperie y agua. Diseños exclusivos de Kaido House, Mini GT, Pop Race y KIDO Garage.</p>",
			Vendor:        "KIDO Accessories",
			ProductType:   "Sticker",
			Scale:         "",
			ApparelSize:   "",
			Tags:          []string{"Accesorios", "Stickers", "Vinyl", "JDM"},
			PriceARS:      3500,
			PriceUSD:      "2.60",
			Status:        "STOCK",
			StockQuantity: 100,
			IsActive:      true,
			GalleryImages: []string{"https://images.unsplash.com/photo-1589384267710-7a170981ca78?auto=format&fit=crop&w=800&q=80"},
			ShortVideoURL: "",
		},
		{
			ID:            999003,
			Title:         "Buzo Hoodie JDM Kanjozoku Night Runner",
			Handle:        "buzo-hoodie-jdm-kanjozoku-night-runner",
			BodyHTML:      "<p>Buzo canguro con frisa invisible premium, interior abrigado y capucha forrada. Gráficos inspirados en el Loop One de Osaka.</p>",
			Vendor:        "KIDO Apparel",
			ProductType:   "Remera",
			ApparelSize:   "XL",
			Scale:         "",
			Tags:          []string{"Indumentaria", "Hoodies", "JDM"},
			PriceARS:      38000,
			PriceUSD:      "28.00",
			Status:        "STOCK",
			StockQuantity: 15,
			IsActive:      true,
			GalleryImages: []string{"https://images.unsplash.com/photo-1556905055-8f358a7a47b2?auto=format&fit=crop&w=800&q=80"},
			ShortVideoURL: "",
		},
	}

	catalog.Products = append(apparelAndStickers, catalog.Products...)
	for idx := range catalog.Products {
		catalog.Products[idx].ID = int64(idx + 1)
	}

	store.mu.Lock()
	store.products = catalog.Products
	store.mu.Unlock()

	log.Printf("Catálogo cargado: %d productos listos para KIDO", len(store.products))
}

// ==========================================
// CONEXIÓN & PERSISTENCIA EN POSTGRESQL
// ==========================================

var (
	db       *sql.DB
	dbActive bool
)

func initDB() {
	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		connStr = "postgres://postgres:715192@localhost:5432/kido_garage?sslmode=disable"
	}

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Printf("⚠️ PostgreSQL: No se pudo crear el pool de conexiones: %v", err)
		return
	}

	if err := db.Ping(); err != nil {
		log.Printf("⚠️ PostgreSQL: No se pudo conectar a localhost:5432/kido_garage: %v", err)
		return
	}

	dbActive = true
	log.Printf("🐘 PostgreSQL CONECTADO EXITOSAMENTE a kido_garage en localhost:5432")
	_, _ = db.Exec("ALTER TABLE raffles ADD COLUMN IF NOT EXISTS winner_number INTEGER")
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url TEXT")
	_, _ = db.Exec(`ALTER TABLE orders 
		ADD COLUMN IF NOT EXISTS shipping_method VARCHAR(100),
		ADD COLUMN IF NOT EXISTS shipping_cost_ars INTEGER DEFAULT 0,
		ADD COLUMN IF NOT EXISTS shipping_postal_code VARCHAR(20),
		ADD COLUMN IF NOT EXISTS shipping_address TEXT,
		ADD COLUMN IF NOT EXISTS tracking_number VARCHAR(100),
		ADD COLUMN IF NOT EXISTS tracking_carrier VARCHAR(50)`)
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS product_types (
		id BIGSERIAL PRIMARY KEY,
		name VARCHAR(100) UNIQUE NOT NULL,
		code VARCHAR(50) UNIQUE NOT NULL,
		is_active BOOLEAN DEFAULT TRUE
	)`)
	_, _ = db.Exec("ALTER TABLE product_types ADD COLUMN IF NOT EXISTS is_active BOOLEAN DEFAULT TRUE")
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS brands (
		id BIGSERIAL PRIMARY KEY,
		name VARCHAR(150) UNIQUE NOT NULL,
		code VARCHAR(50) UNIQUE NOT NULL,
		carrusel BOOLEAN DEFAULT TRUE,
		imagen TEXT DEFAULT ''
	)`)
	_, _ = db.Exec("ALTER TABLE brands ADD COLUMN IF NOT EXISTS carrusel BOOLEAN DEFAULT TRUE")
	_, _ = db.Exec("ALTER TABLE brands ADD COLUMN IF NOT EXISTS imagen TEXT DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE products ADD COLUMN IF NOT EXISTS brand_id BIGINT REFERENCES brands(id)")
	_, _ = db.Exec("ALTER TABLE products ADD COLUMN IF NOT EXISTS product_type_id BIGINT REFERENCES product_types(id)")
	_, _ = db.Exec("ALTER TABLE products ADD COLUMN IF NOT EXISTS in_slider BOOLEAN DEFAULT FALSE")
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS site_settings (
		key VARCHAR(100) PRIMARY KEY,
		value TEXT NOT NULL
	)`)
	var sliderMaxVal string
	if err := db.QueryRow("SELECT value FROM site_settings WHERE key = 'slider_max_items'").Scan(&sliderMaxVal); err == nil {
		if n, err := strconv.Atoi(sliderMaxVal); err == nil && n > 0 {
			store.sliderConfig.MaxItems = n
		}
	}
	var sliderActiveVal string
	if err := db.QueryRow("SELECT value FROM site_settings WHERE key = 'slider_active'").Scan(&sliderActiveVal); err == nil {
		store.sliderConfig.Active = sliderActiveVal == "true" || sliderActiveVal == "1"
	}

	// 0. Sincronizar product_types y brands
	var ptCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM product_types").Scan(&ptCount); err == nil {
		if ptCount == 0 {
			store.mu.RLock()
			for _, pt := range store.productTypes {
				_, _ = db.Exec(`INSERT INTO product_types (id, name, code, is_active) VALUES ($1, $2, $3, $4) ON CONFLICT (name) DO NOTHING`, pt.ID, pt.Name, pt.Code, pt.IsActive)
			}
			store.mu.RUnlock()
		} else {
			rows, err := db.Query("SELECT id, name, code, COALESCE(is_active, true) FROM product_types ORDER BY id DESC")
			if err == nil {
				var pgTypes []ProductTypeModel
				for rows.Next() {
					var t ProductTypeModel
					if err := rows.Scan(&t.ID, &t.Name, &t.Code, &t.IsActive); err == nil {
						pgTypes = append(pgTypes, t)
					}
				}
				rows.Close()
				if len(pgTypes) > 0 {
					store.mu.Lock()
					store.productTypes = pgTypes
					store.mu.Unlock()
				}
			}
		}
	}

	var brandCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM brands").Scan(&brandCount); err == nil {
		if brandCount == 0 {
			store.mu.RLock()
			for _, b := range store.brands {
				_, _ = db.Exec(`INSERT INTO brands (id, name, code, carrusel, imagen) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (name) DO UPDATE SET carrusel=EXCLUDED.carrusel, imagen=EXCLUDED.imagen`, b.ID, b.Name, b.Code, b.Carrusel, b.Imagen)
			}
			store.mu.RUnlock()
			log.Printf("🐘 [PostgreSQL] Sembradas %d marcas en tabla 'brands'", len(store.brands))
		} else {
			rows, err := db.Query("SELECT id, name, code, COALESCE(carrusel, true), COALESCE(imagen, '') FROM brands ORDER BY id DESC")
			if err == nil {
				var pgBrands []Brand
				for rows.Next() {
					var b Brand
					if err := rows.Scan(&b.ID, &b.Name, &b.Code, &b.Carrusel, &b.Imagen); err == nil {
						pgBrands = append(pgBrands, b)
					}
				}
				rows.Close()
				if len(pgBrands) > 0 {
					store.mu.Lock()
					store.brands = pgBrands
					store.mu.Unlock()
					log.Printf("🐘 [PostgreSQL] Cargadas %d marcas desde la base de datos", len(pgBrands))
				}
			}
		}
	}

	// 1. Sincronizar usuarios
	var userCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err == nil {
		if userCount == 0 {
			store.mu.RLock()
			for _, u := range store.users {
				pwdHash := u.Password
				if !strings.HasPrefix(pwdHash, "$2") {
					if h, err := hashPassword(pwdHash); err == nil {
						pwdHash = h
					}
				}
				_, _ = db.Exec(`INSERT INTO users (id, email, password_hash, role, first_name, last_name, phone, locality, street, street_number, avatar_url, consecutive_months_buying, is_frequent_customer, frequent_points, total_purchases_count)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
				ON CONFLICT (id) DO NOTHING`,
					u.ID, u.Email, pwdHash, u.Role, u.FirstName, u.LastName, u.Phone, u.Locality, u.Street, u.StreetNumber, u.AvatarURL, u.ConsecutiveMonths, u.IsFrequentCustomer, u.FrequentPoints, u.TotalPurchasesCount)
			}
			store.mu.RUnlock()
			log.Printf("🐘 [PostgreSQL] Sembrados %d usuarios en tabla 'users'", len(store.users))
		} else {
			rows, err := db.Query("SELECT id, email, COALESCE(password_hash,''), role, COALESCE(first_name,''), COALESCE(last_name,''), COALESCE(phone,''), COALESCE(locality,''), COALESCE(street,''), COALESCE(street_number,''), COALESCE(avatar_url,''), consecutive_months_buying, is_frequent_customer, frequent_points, total_purchases_count FROM users ORDER BY id DESC")
			if err == nil {
				var pgUsers []User
				for rows.Next() {
					var u User
					if err := rows.Scan(&u.ID, &u.Email, &u.Password, &u.Role, &u.FirstName, &u.LastName, &u.Phone, &u.Locality, &u.Street, &u.StreetNumber, &u.AvatarURL, &u.ConsecutiveMonths, &u.IsFrequentCustomer, &u.FrequentPoints, &u.TotalPurchasesCount); err == nil {
						pgUsers = append(pgUsers, u)
					}
				}
				rows.Close()
				if len(pgUsers) > 0 {
					store.mu.Lock()
					store.users = pgUsers
					store.mu.Unlock()
					log.Printf("🐘 [PostgreSQL] Cargados %d usuarios desde la base de datos", len(pgUsers))
				}
			}
		}

		// Sincronizar contraseña de administrador configurada
		if h, err := hashPassword("Lujo$2404"); err == nil {
			_, _ = db.Exec("UPDATE users SET password_hash = $1 WHERE LOWER(email) = 'admin@kido.com.ar'", h)
			store.mu.Lock()
			for i := range store.users {
				if strings.EqualFold(store.users[i].Email, "admin@kido.com.ar") {
					store.users[i].Password = h
				}
			}
			store.mu.Unlock()
		}
	}

	// 2. Sincronizar catálogo de productos
	var prodCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM products").Scan(&prodCount); err == nil {
		if prodCount == 0 {
			store.mu.RLock()
			for _, p := range store.products {
				imgJSON, _ := json.Marshal(p.GalleryImages)
				_, _ = db.Exec(`INSERT INTO products (id, internal_code, title, handle, product_type, product_type_id, scale, apparel_size, vendor, brand_id, price_ars, price_usd, stock_quantity, status, is_active, has_chase_chance, gallery_images, short_video_url, description, in_slider)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
				ON CONFLICT (id) DO NOTHING`,
					p.ID, p.InternalCode, p.Title, p.Handle, p.ProductType, p.ProductTypeID, p.Scale, p.ApparelSize, p.Vendor, p.BrandID, p.PriceARS, p.PriceUSD, p.StockQuantity, p.Status, p.IsActive, p.HasChaseChance, string(imgJSON), p.ShortVideoURL, p.BodyHTML, p.InSlider)
			}
			store.mu.RUnlock()
			log.Printf("🐘 [PostgreSQL] Sembrados %d productos en tabla 'products'", len(store.products))
		} else {
			rows, err := db.Query("SELECT id, COALESCE(internal_code,''), title, handle, product_type, product_type_id, COALESCE(scale,''), COALESCE(apparel_size,''), vendor, brand_id, price_ars, price_usd, stock_quantity, status, is_active, has_chase_chance, COALESCE(gallery_images::text,'[]'), COALESCE(short_video_url,''), COALESCE(description,''), COALESCE(in_slider, false) FROM products ORDER BY id DESC")
			if err == nil {
				var pgProducts []Product
				for rows.Next() {
					var p Product
					var imgRaw string
					var brandID, typeID sql.NullInt64
					if err := rows.Scan(&p.ID, &p.InternalCode, &p.Title, &p.Handle, &p.ProductType, &typeID, &p.Scale, &p.ApparelSize, &p.Vendor, &brandID, &p.PriceARS, &p.PriceUSD, &p.StockQuantity, &p.Status, &p.IsActive, &p.HasChaseChance, &imgRaw, &p.ShortVideoURL, &p.BodyHTML, &p.InSlider); err == nil {
						if brandID.Valid {
							b := brandID.Int64
							p.BrandID = &b
						}
						if typeID.Valid {
							t := typeID.Int64
							p.ProductTypeID = &t
						}
						_ = json.Unmarshal([]byte(imgRaw), &p.GalleryImages)
						if len(p.GalleryImages) > 0 {
							p.Images = []ProductImage{{ID: 1, Position: 1, Src: p.GalleryImages[0]}}
						}
						pgProducts = append(pgProducts, p)
					}
				}
				rows.Close()
				if len(pgProducts) > 0 {
					store.mu.Lock()
					store.products = pgProducts
					store.mu.Unlock()
					log.Printf("🐘 [PostgreSQL] Cargados %d productos desde la base de datos", len(pgProducts))
				}
			}
		}
	}

	// 3. Sincronizar gastos
	var expCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM expenses").Scan(&expCount); err == nil && expCount == 0 {
		store.mu.RLock()
		for _, e := range store.expenses {
			_, _ = db.Exec(`INSERT INTO expenses (id, category, concept, supplier, amount_ars, amount_usd, invoice_number, expense_date)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (id) DO NOTHING`,
				e.ID, e.Category, e.Concept, e.Supplier, e.AmountARS, e.AmountUSD, e.InvoiceNumber, e.Date)
		}
		store.mu.RUnlock()
		log.Printf("🐘 [PostgreSQL] Sembrados %d gastos en tabla 'expenses'", len(store.expenses))
	}

	// 4. Sincronizar rifas
	var rafCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM raffles").Scan(&rafCount); err == nil && rafCount == 0 {
		store.mu.RLock()
		for _, r := range store.raffles {
			imgJSON, _ := json.Marshal(r.PrizeImages)
			startT, _ := time.Parse("2006-01-02 15:04", r.StartDatetime)
			if startT.IsZero() {
				startT = time.Now()
			}
			endT, _ := time.Parse("2006-01-02 15:04", r.EndDatetime)
			if endT.IsZero() {
				endT = time.Now().AddDate(0, 1, 0)
			}
			drawT, _ := time.Parse("2006-01-02 15:04", r.DrawDatetime)
			if drawT.IsZero() {
				drawT = time.Now().AddDate(0, 1, 5)
			}

			_, _ = db.Exec(`INSERT INTO raffles (id, raffle_number, title, prize_description, prize_images, start_datetime, end_datetime, draw_datetime, min_number, max_number, ticket_price_ars, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (id) DO NOTHING`,
				r.ID, r.RaffleNumber, r.Title, r.PrizeDescription, string(imgJSON), startT, endT, drawT, r.MinNumber, r.MaxNumber, r.TicketPriceARS, r.Status)
		}
		store.mu.RUnlock()
		log.Printf("🐘 [PostgreSQL] Sembradas %d rifas en tabla 'raffles'", len(store.raffles))
	}

	// Sincronizar secuencias autonuméricas de PostgreSQL (1 en 1)
	tableSeqs := []string{"users", "products", "stock_movements", "orders", "order_items", "partial_payments", "raffles", "raffle_tickets", "expenses", "purchase_orders", "brands", "product_types"}
	for _, tbl := range tableSeqs {
		_, _ = db.Exec(fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM %s WHERE id < 1000000000), 0) + 1, false)`, tbl, tbl))
	}
}

func pgSaveUser(u User) {
	if !dbActive || db == nil {
		return
	}
	go func() {
		pwdHash := u.Password
		if pwdHash != "" && !strings.HasPrefix(pwdHash, "$2") {
			if h, err := hashPassword(pwdHash); err == nil {
				pwdHash = h
			}
		}
		_, err := db.Exec(`INSERT INTO users (id, email, password_hash, role, first_name, last_name, phone, locality, street, street_number, avatar_url, consecutive_months_buying, is_frequent_customer, frequent_points, total_purchases_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (id) DO UPDATE SET
			email = EXCLUDED.email,
			password_hash = CASE WHEN EXCLUDED.password_hash != '' THEN EXCLUDED.password_hash ELSE users.password_hash END,
			role = EXCLUDED.role,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			phone = EXCLUDED.phone,
			locality = EXCLUDED.locality,
			street = EXCLUDED.street,
			street_number = EXCLUDED.street_number,
			avatar_url = EXCLUDED.avatar_url,
			consecutive_months_buying = EXCLUDED.consecutive_months_buying,
			is_frequent_customer = EXCLUDED.is_frequent_customer,
			frequent_points = EXCLUDED.frequent_points,
			total_purchases_count = EXCLUDED.total_purchases_count,
			updated_at = CURRENT_TIMESTAMP`,
			u.ID, u.Email, pwdHash, u.Role, u.FirstName, u.LastName, u.Phone, u.Locality, u.Street, u.StreetNumber, u.AvatarURL, u.ConsecutiveMonths, u.IsFrequentCustomer, u.FrequentPoints, u.TotalPurchasesCount)
		if err != nil {
			log.Printf("⚠️ Error guardando usuario en PostgreSQL: %v", err)
		}
	}()
}

func pgSaveProduct(p Product) {
	if !dbActive || db == nil {
		return
	}
	go func() {
		imgJSON, _ := json.Marshal(p.GalleryImages)
		_, err := db.Exec(`INSERT INTO products (id, internal_code, title, handle, product_type, product_type_id, scale, apparel_size, vendor, brand_id, price_ars, price_usd, stock_quantity, status, is_active, has_chase_chance, gallery_images, short_video_url, description, in_slider)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		ON CONFLICT (id) DO UPDATE SET
			internal_code = EXCLUDED.internal_code,
			title = EXCLUDED.title,
			handle = EXCLUDED.handle,
			product_type = EXCLUDED.product_type,
			product_type_id = EXCLUDED.product_type_id,
			scale = EXCLUDED.scale,
			apparel_size = EXCLUDED.apparel_size,
			vendor = EXCLUDED.vendor,
			brand_id = EXCLUDED.brand_id,
			stock_quantity = EXCLUDED.stock_quantity,
			status = EXCLUDED.status,
			is_active = EXCLUDED.is_active,
			price_ars = EXCLUDED.price_ars,
			price_usd = EXCLUDED.price_usd,
			has_chase_chance = EXCLUDED.has_chase_chance,
			gallery_images = EXCLUDED.gallery_images,
			short_video_url = EXCLUDED.short_video_url,
			description = EXCLUDED.description,
			in_slider = EXCLUDED.in_slider,
			updated_at = CURRENT_TIMESTAMP`,
			p.ID, p.InternalCode, p.Title, p.Handle, p.ProductType, p.ProductTypeID, p.Scale, p.ApparelSize, p.Vendor, p.BrandID, p.PriceARS, p.PriceUSD, p.StockQuantity, p.Status, p.IsActive, p.HasChaseChance, string(imgJSON), p.ShortVideoURL, p.BodyHTML, p.InSlider)
		if err != nil {
			log.Printf("⚠️ Error guardando producto en PostgreSQL: %v", err)
		}
	}()
}

func pgSaveOrder(o Order) {
	if !dbActive || db == nil {
		return
	}
	go func() {
		var custID interface{} = nil
		if o.CustomerID > 0 {
			var exists bool
			_ = db.QueryRow("SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)", o.CustomerID).Scan(&exists)
			if exists {
				custID = o.CustomerID
			}
		}
		if custID == nil && o.CustomerEmail != "" {
			var uid int64
			err := db.QueryRow("SELECT id FROM users WHERE LOWER(email) = LOWER($1)", o.CustomerEmail).Scan(&uid)
			if err == nil && uid > 0 {
				custID = uid
			}
		}

		_, err := db.Exec(`INSERT INTO orders (id, order_number, customer_id, total_ars, total_paid_ars, remaining_balance_ars, is_fully_paid, delivery_status, order_type, shipping_method, shipping_cost_ars, shipping_postal_code, shipping_address, tracking_number, tracking_carrier)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (id) DO UPDATE SET
			total_paid_ars = EXCLUDED.total_paid_ars,
			remaining_balance_ars = EXCLUDED.remaining_balance_ars,
			is_fully_paid = EXCLUDED.is_fully_paid,
			delivery_status = EXCLUDED.delivery_status,
			shipping_method = EXCLUDED.shipping_method,
			shipping_cost_ars = EXCLUDED.shipping_cost_ars,
			shipping_postal_code = EXCLUDED.shipping_postal_code,
			shipping_address = EXCLUDED.shipping_address,
			tracking_number = EXCLUDED.tracking_number,
			tracking_carrier = EXCLUDED.tracking_carrier,
			updated_at = CURRENT_TIMESTAMP`,
			o.ID, o.OrderNumber, custID, o.TotalARS, o.TotalPaidARS, o.RemainingBalanceARS, o.IsFullyPaid, o.DeliveryStatus, o.OrderType,
			o.ShippingMethod, o.ShippingCostARS, o.ShippingPostalCode, o.ShippingAddress, o.TrackingNumber, o.TrackingCarrier)
		if err != nil {
			log.Printf("⚠️ Error guardando orden en PostgreSQL: %v", err)
		}

		for _, it := range o.Items {
			_, _ = db.Exec(`INSERT INTO order_items (order_id, product_id, product_title, quantity, unit_price_ars, subtotal_ars)
			VALUES ($1, $2, $3, $4, $5, $6)`,
				o.ID, it.ProductID, it.ProductTitle, it.Quantity, it.UnitPriceARS, it.SubtotalARS)
		}

		for _, pm := range o.Payments {
			_, _ = db.Exec(`INSERT INTO partial_payments (id, order_id, product_id, amount_ars, payment_method, mercadopago_payment_id, mercadopago_status, is_downpayment)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (id) DO NOTHING`,
				pm.ID, o.ID, pm.ProductID, pm.AmountARS, pm.PaymentMethod, pm.MercadoPagoID, pm.PaymentStatus, pm.IsDownPayment)
		}
	}()
}

func pgSaveRaffle(r Raffle) {
	if !dbActive || db == nil {
		return
	}
	go func() {
		imgJSON, _ := json.Marshal(r.PrizeImages)
		startT, _ := time.Parse("2006-01-02 15:04", r.StartDatetime)
		if startT.IsZero() {
			startT = time.Now()
		}
		endT, _ := time.Parse("2006-01-02 15:04", r.EndDatetime)
		if endT.IsZero() {
			endT = time.Now().AddDate(0, 1, 0)
		}
		drawT, _ := time.Parse("2006-01-02 15:04", r.DrawDatetime)
		var winnerNum interface{} = nil
		if r.WinnerNumber != nil {
			winnerNum = *r.WinnerNumber
		}

		_, _ = db.Exec(`INSERT INTO raffles (id, raffle_number, title, prize_description, prize_images, start_datetime, end_datetime, draw_datetime, min_number, max_number, ticket_price_ars, status, winner_number)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			winner_number = EXCLUDED.winner_number`,
			r.ID, r.RaffleNumber, r.Title, r.PrizeDescription, string(imgJSON), startT, endT, drawT, r.MinNumber, r.MaxNumber, r.TicketPriceARS, r.Status, winnerNum)
	}()
}

func pgSaveRaffleTicket(raffleID int64, t RaffleTicket) {
	if !dbActive || db == nil {
		return
	}
	go func() {
		_, _ = db.Exec(`INSERT INTO raffle_tickets (raffle_id, ticket_number, customer_id, customer_email, is_free_frequent_ticket, mercadopago_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (raffle_id, ticket_number) DO NOTHING`,
			raffleID, t.Number, t.CustomerID, t.CustomerEmail, t.IsFreeTicket, t.MercadoPagoID)
	}()
}

func pgSaveExpense(e Expense) {
	if !dbActive || db == nil {
		return
	}
	go func() {
		_, _ = db.Exec(`INSERT INTO expenses (id, category, concept, supplier, amount_ars, amount_usd, invoice_number, expense_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO NOTHING`,
			e.ID, e.Category, e.Concept, e.Supplier, e.AmountARS, e.AmountUSD, e.InvoiceNumber, e.Date)
	}()
}

// ==========================================
// CONFIGURACIÓN MERCADO PAGO (CHECKOUT PRO & WEBHOOKS)
// ==========================================

type MPConfig struct {
	AccessToken string `json:"access_token"`
	PublicKey   string `json:"public_key"`
	IsSandbox   bool   `json:"is_sandbox"`
}

var mpConfig = MPConfig{
	AccessToken: os.Getenv("MP_ACCESS_TOKEN"),
	PublicKey:   os.Getenv("MP_PUBLIC_KEY"),
	IsSandbox:   true,
}

func loadMPConfig() {
	data, err := os.ReadFile("mp_config.json")
	if err == nil {
		var cfg MPConfig
		if json.Unmarshal(data, &cfg) == nil {
			mpConfig = cfg
		}
	}
	if mpConfig.AccessToken != "" {
		log.Printf("💳 Mercado Pago CONFIGURADO (Sandbox: %v, Token: %s)", mpConfig.IsSandbox, maskToken(mpConfig.AccessToken))
	} else {
		log.Printf("💳 Mercado Pago en MODO SIMULACIÓN / DEMO (Access Token no configurado aún)")
	}
}

func saveMPConfig(cfg MPConfig) error {
	mpConfig = cfg
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("mp_config.json", data, 0644)
}

func maskToken(t string) string {
	if len(t) <= 10 {
		return "****"
	}
	return t[:8] + "..." + t[len(t)-4:]
}

func createMPPreference(order Order, amountARS int, baseURL string) (string, string, string, error) {
	if mpConfig.AccessToken == "" {
		return "", "", "", nil
	}

	title := fmt.Sprintf("Pedido %s - KIDO Garage", order.OrderNumber)
	if order.OrderType == "PRE_VENTA_CON_SEÑA" && order.RemainingBalanceARS > 0 {
		title = fmt.Sprintf("Seña 30%% Pedido %s - KIDO Garage", order.OrderNumber)
	}

	notificationURL := ""
	if strings.HasPrefix(baseURL, "https://") && !strings.Contains(baseURL, "localhost") {
		notificationURL = baseURL + "/api/webhooks/mercadopago"
	}

	prefReq := map[string]interface{}{
		"items": []map[string]interface{}{
			{
				"id":          strconv.FormatInt(order.ID, 10),
				"title":       title,
				"description": fmt.Sprintf("%d artículos coleccionables en KIDO Garage", len(order.Items)),
				"quantity":    1,
				"unit_price":  float64(amountARS),
				"currency_id": "ARS",
			},
		},
		"payer": map[string]interface{}{
			"name":  order.CustomerName,
			"email": order.CustomerEmail,
		},
		"back_urls": map[string]string{
			"success": baseURL + "/index.html?payment=success&order_id=" + strconv.FormatInt(order.ID, 10),
			"failure": baseURL + "/index.html?payment=failure&order_id=" + strconv.FormatInt(order.ID, 10),
			"pending": baseURL + "/index.html?payment=pending&order_id=" + strconv.FormatInt(order.ID, 10),
		},
		"auto_return":          "approved",
		"external_reference":   order.OrderNumber,
		"statement_descriptor": "KIDO GARAGE",
	}

	if notificationURL != "" {
		prefReq["notification_url"] = notificationURL
	}

	bodyBytes, err := json.Marshal(prefReq)
	if err != nil {
		return "", "", "", err
	}

	req, err := http.NewRequest("POST", "https://api.mercadopago.com/checkout/preferences", bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+mpConfig.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	var prefResp struct {
		ID               string `json:"id"`
		InitPoint        string `json:"init_point"`
		SandboxInitPoint string `json:"sandbox_init_point"`
		Message          string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&prefResp); err != nil {
		return "", "", "", err
	}

	chosenInitPoint := prefResp.InitPoint
	if mpConfig.IsSandbox && prefResp.SandboxInitPoint != "" {
		chosenInitPoint = prefResp.SandboxInitPoint
	}

	return prefResp.ID, chosenInitPoint, prefResp.SandboxInitPoint, nil
}

func processMPPayment(paymentID string) {
	req, err := http.NewRequest("GET", "https://api.mercadopago.com/v1/payments/"+paymentID, nil)
	if err != nil {
		log.Printf("⚠️ [Webhook MP] Error armando request para pago %s: %v", paymentID, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+mpConfig.AccessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("⚠️ [Webhook MP] Error consultando pago %s a MP: %v", paymentID, err)
		return
	}
	defer resp.Body.Close()

	var mpPayment struct {
		ID                int64   `json:"id"`
		Status            string  `json:"status"` // "approved"
		StatusDetail      string  `json:"status_detail"`
		TransactionAmount float64 `json:"transaction_amount"`
		PaymentMethodID   string  `json:"payment_method_id"`
		ExternalReference string  `json:"external_reference"` // order_number ej: KIDO-ORD-12345
	}
	if err := json.NewDecoder(resp.Body).Decode(&mpPayment); err != nil {
		log.Printf("⚠️ [Webhook MP] Error decodificando pago %s: %v", paymentID, err)
		return
	}

	log.Printf("🔔 [Webhook MP] Notificación: Pago=%d Status=%s Monto=$%.0f Ref=%s", mpPayment.ID, mpPayment.Status, mpPayment.TransactionAmount, mpPayment.ExternalReference)

	if mpPayment.Status == "approved" {
		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.orders {
			if store.orders[i].OrderNumber == mpPayment.ExternalReference || strconv.FormatInt(store.orders[i].ID, 10) == mpPayment.ExternalReference {
				ord := &store.orders[i]

				alreadyRecorded := false
				for _, p := range ord.Payments {
					if p.MercadoPagoID == strconv.FormatInt(mpPayment.ID, 10) {
						alreadyRecorded = true
						break
					}
				}

				if !alreadyRecorded {
					paidAmount := int(mpPayment.TransactionAmount)
					if paidAmount > ord.RemainingBalanceARS {
						paidAmount = ord.RemainingBalanceARS
					}

					ord.TotalPaidARS += paidAmount
					ord.RemainingBalanceARS -= paidAmount
					if ord.RemainingBalanceARS <= 0 {
						ord.RemainingBalanceARS = 0
						ord.IsFullyPaid = true
						ord.DeliveryStatus = "LISTO_PARA_DESPACHAR"
					}

					payment := PartialPayment{
						ID:            time.Now().UnixNano(),
						OrderID:       ord.ID,
						AmountARS:     paidAmount,
						PaymentMethod: "Mercado Pago (" + mpPayment.PaymentMethodID + ")",
						MercadoPagoID: strconv.FormatInt(mpPayment.ID, 10),
						PaymentStatus: "approved",
						IsDownPayment: ord.OrderType == "PRE_VENTA_CON_SEÑA",
						CreatedAt:     time.Now(),
					}
					ord.Payments = append(ord.Payments, payment)
					pgSaveOrder(*ord)

					log.Printf("✅ [Webhook MP] ¡Pago aprobado procesado con éxito! Pedido=%s TotalAbonado=$%d Saldo=$%d Estado=%s", ord.OrderNumber, ord.TotalPaidARS, ord.RemainingBalanceARS, ord.DeliveryStatus)
				}
				return
			}
		}
	}
}

// ==========================================
// SERVIDOR HTTP & APIS
// ==========================================

func main() {
	initSessionSecret()
	loadCatalog()
	initRaffles()
	initOrders()
	initDB()
	loadMPConfig()

	mux := http.NewServeMux()

	// 1. Archivos estáticos en /public
	publicDir := "./public"
	if fi, err := os.Stat(publicDir); err != nil || !fi.IsDir() {
		exePath, err := os.Executable()
		if err == nil {
			cand := filepath.Join(filepath.Dir(exePath), "public")
			if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
				publicDir = cand
			}
		}
	}
	fs := http.FileServer(http.Dir(publicDir))
	mux.Handle("/", fs)

	// ==========================================
	// AUTENTICACIÓN & PERFILES
	// ==========================================

	// Registro de Cliente
	mux.HandleFunc("/api/auth/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Email        string `json:"email"`
			Password     string `json:"password"`
			FirstName    string `json:"first_name"`
			LastName     string `json:"last_name"`
			Phone        string `json:"phone"`
			Locality     string `json:"locality"`
			Street       string `json:"street"`
			StreetNumber string `json:"street_number"`
			AvatarURL    string `json:"avatar_url"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Email == "" || req.Password == "" {
			http.Error(w, "Correo y contraseña requeridos", http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for _, u := range store.users {
			if strings.EqualFold(u.Email, req.Email) {
				http.Error(w, "El correo electrónico ya está registrado", http.StatusConflict)
				return
			}
		}

		avatar := req.AvatarURL
		if avatar == "" {
			avatar = "https://images.unsplash.com/photo-1535713875002-d1d0cf377fde?auto=format&fit=crop&w=200&q=80"
		}

		hashedPassword, err := hashPassword(req.Password)
		if err != nil {
			http.Error(w, "Error procesando contraseña", http.StatusInternalServerError)
			return
		}

		var maxID int64 = 0
		if dbActive && db != nil {
			_ = db.QueryRow("SELECT COALESCE(MAX(id), 0) FROM users WHERE id < 1000000000").Scan(&maxID)
		}
		for _, eu := range store.users {
			if eu.ID > maxID && eu.ID < 1000000000 {
				maxID = eu.ID
			}
		}

		newUser := User{
			ID:                  maxID + 1,
			Email:               req.Email,
			Password:            hashedPassword,
			Role:                "CLIENT",
			FirstName:           req.FirstName,
			LastName:            req.LastName,
			Phone:               req.Phone,
			Locality:            req.Locality,
			Street:              req.Street,
			StreetNumber:        req.StreetNumber,
			AvatarURL:           avatar,
			ConsecutiveMonths:   1, // Primer mes de registro
			IsFrequentCustomer:  false,
			FrequentPoints:      0,
			TotalPurchasesCount: 0,
		}

		store.users = append(store.users, newUser)
		pgSaveUser(newUser)

		// Generar token y cookie de sesión
		token := generateSessionToken(newUser)
		http.SetCookie(w, &http.Cookie{
			Name:     "kido_session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Now().Add(7 * 24 * time.Hour),
		})

		// Ocultar hash en la respuesta al cliente
		userResp := newUser
		userResp.Password = ""

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"user":    userResp,
			"token":   token,
			"message": "¡Cuenta de cliente creada exitosamente!",
		})
	})

	// Login con Rate Limiting y Protección contra Fuerza Bruta
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		ip := getClientIP(r)
		if globalLimiter.IsLoginBlocked(ip) {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Demasiados intentos fallidos. Tu IP está temporalmente bloqueada por 5 minutos por seguridad.",
			})
			return
		}

		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.users {
			u := &store.users[i]
			if strings.EqualFold(u.Email, req.Email) {
				if checkPasswordHash(req.Password, u.Password) {
					// Resetear contador de fallos para esta IP
					globalLimiter.ResetFailedLogins(ip)

					// Auto-migración si estaba en texto plano
					if !strings.HasPrefix(u.Password, "$2") {
						if newH, err := hashPassword(req.Password); err == nil {
							u.Password = newH
							pgSaveUser(*u)
						}
					}

					token := generateSessionToken(*u)
					http.SetCookie(w, &http.Cookie{
						Name:     "kido_session",
						Value:    token,
						Path:     "/",
						HttpOnly: true,
						SameSite: http.SameSiteLaxMode,
						Expires:  time.Now().Add(7 * 24 * time.Hour),
					})

					userResp := *u
					userResp.Password = ""

					json.NewEncoder(w).Encode(map[string]interface{}{
						"success": true,
						"user":    userResp,
						"token":   token,
					})
					return
				}
				break
			}
		}

		// Registrar fallo de autenticación para rate limiting
		globalLimiter.RecordFailedLogin(ip)

		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Credenciales incorrectas",
		})
	})

	// Registro/Login con Google
	mux.HandleFunc("/api/auth/google", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Email     string `json:"email"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
			AvatarURL string `json:"avatar_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for _, u := range store.users {
			if strings.EqualFold(u.Email, req.Email) {
				token := generateSessionToken(u)
				http.SetCookie(w, &http.Cookie{
					Name:     "kido_session",
					Value:    token,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					Expires:  time.Now().Add(7 * 24 * time.Hour),
				})
				userResp := u
				userResp.Password = ""
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"user":    userResp,
					"token":   token,
					"message": "Sesión iniciada con Google",
				})
				return
			}
		}

		// Crear nuevo usuario desde Google
		var maxID int64 = 0
		if dbActive && db != nil {
			_ = db.QueryRow("SELECT COALESCE(MAX(id), 0) FROM users WHERE id < 1000000000").Scan(&maxID)
		}
		for _, eu := range store.users {
			if eu.ID > maxID && eu.ID < 1000000000 {
				maxID = eu.ID
			}
		}

		newUser := User{
			ID:                  maxID + 1,
			Email:               req.Email,
			Role:                "CLIENT",
			GoogleID:            "goog_" + strconv.FormatInt(time.Now().Unix(), 10),
			FirstName:           req.FirstName,
			LastName:            req.LastName,
			Locality:            "CABA",
			Street:              "Av. Corrientes",
			StreetNumber:        "1200",
			AvatarURL:           req.AvatarURL,
			ConsecutiveMonths:   1,
			IsFrequentCustomer:  false,
			FrequentPoints:      0,
			TotalPurchasesCount: 0,
		}
		store.users = append(store.users, newUser)

		token := generateSessionToken(newUser)
		http.SetCookie(w, &http.Cookie{
			Name:     "kido_session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Now().Add(7 * 24 * time.Hour),
		})

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"user":    newUser,
			"token":   token,
			"message": "Registro completado con cuenta de Google",
		})
	})

	// Obtener usuario en sesión actual (/api/auth/me)
	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		user, err := getAuthenticatedUser(r)
		if err != nil || user == nil {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "No autenticado",
			})
			return
		}

		userResp := *user
		userResp.Password = ""
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"user":    userResp,
		})
	})

	// Cerrar sesión (/api/auth/logout)
	mux.HandleFunc("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.SetCookie(w, &http.Cookie{
			Name:     "kido_session",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Unix(0, 0),
			MaxAge:   -1,
		})
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Sesión cerrada exitosamente",
		})
	})

	// Actualizar Perfil de Usuario
	mux.HandleFunc("/api/auth/profile", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req User
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.users {
			if store.users[i].ID == req.ID || strings.EqualFold(store.users[i].Email, req.Email) {
				if req.FirstName != "" {
					store.users[i].FirstName = req.FirstName
				}
				if req.LastName != "" {
					store.users[i].LastName = req.LastName
				}
				if req.Phone != "" {
					store.users[i].Phone = req.Phone
				}
				if req.Locality != "" {
					store.users[i].Locality = req.Locality
				}
				if req.Street != "" {
					store.users[i].Street = req.Street
				}
				if req.StreetNumber != "" {
					store.users[i].StreetNumber = req.StreetNumber
				}
				if req.AvatarURL != "" {
					store.users[i].AvatarURL = req.AvatarURL
				}
				updatedUser := store.users[i]
				pgSaveUser(updatedUser)
				userResp := updatedUser
				userResp.Password = ""
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"user":    userResp,
				})
				return
			}
		}

		http.Error(w, "Usuario no encontrado", http.StatusNotFound)
	})

	// Cambiar Contraseña (/api/auth/change-password)
	mux.HandleFunc("/api/auth/change-password", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		user, err := getAuthenticatedUser(r)
		if err != nil || user == nil {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Autenticación requerida para cambiar contraseña",
			})
			return
		}

		var req struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
			TargetEmail     string `json:"target_email,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		req.NewPassword = strings.TrimSpace(req.NewPassword)
		if len(req.NewPassword) < 6 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "La nueva contraseña debe tener al menos 6 caracteres",
			})
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		targetEmail := user.Email
		if req.TargetEmail != "" && strings.EqualFold(user.Role, "ADMIN") {
			targetEmail = req.TargetEmail
		}

		var targetIndex int = -1
		for i := range store.users {
			if strings.EqualFold(store.users[i].Email, targetEmail) {
				targetIndex = i
				break
			}
		}

		if targetIndex == -1 {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Usuario no encontrado",
			})
			return
		}

		// Si es cambio de clave propia, validar clave actual
		if req.TargetEmail == "" || strings.EqualFold(targetEmail, user.Email) {
			if !checkPasswordHash(req.CurrentPassword, store.users[targetIndex].Password) {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": false,
					"error":   "La contraseña actual es incorrecta",
				})
				return
			}
		}

		newHash, err := hashPassword(req.NewPassword)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Error procesando la nueva contraseña",
			})
			return
		}

		store.users[targetIndex].Password = newHash
		pgSaveUser(store.users[targetIndex])

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Contraseña actualizada exitosamente",
		})
	})

	// ==========================================
	// RANKINGS & GAMIFICACIÓN (CLIENTE FRECUENTE)
	// ==========================================
	mux.HandleFunc("/api/rankings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		store.mu.RLock()
		defer store.mu.RUnlock()

		var clients []User
		for _, u := range store.users {
			if u.Role == "CLIENT" {
				clients = append(clients, u)
			}
		}

		// Ranking 1: Por Puntos de Meses Frecuentes
		type FrequentRankItem struct {
			Email             string `json:"email"`
			Name              string `json:"name"`
			AvatarURL         string `json:"avatar_url"`
			ConsecutiveMonths int    `json:"consecutive_months"`
			FrequentPoints    int    `json:"frequent_points"`
			IsFrequent        bool   `json:"is_frequent"`
		}
		var frequentRanking []FrequentRankItem
		for _, c := range clients {
			frequentRanking = append(frequentRanking, FrequentRankItem{
				Email:             c.Email,
				Name:              c.FirstName + " " + c.LastName,
				AvatarURL:         c.AvatarURL,
				ConsecutiveMonths: c.ConsecutiveMonths,
				FrequentPoints:    c.FrequentPoints,
				IsFrequent:        c.IsFrequentCustomer,
			})
		}

		// Ranking 2: Por Cantidad Total de Compras Hechas
		type PurchasesRankItem struct {
			Email          string `json:"email"`
			Name           string `json:"name"`
			AvatarURL      string `json:"avatar_url"`
			TotalPurchases int    `json:"total_purchases"`
		}
		var purchasesRanking []PurchasesRankItem
		for _, c := range clients {
			purchasesRanking = append(purchasesRanking, PurchasesRankItem{
				Email:          c.Email,
				Name:           c.FirstName + " " + c.LastName,
				AvatarURL:      c.AvatarURL,
				TotalPurchases: c.TotalPurchasesCount,
			})
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"frequent_ranking":  frequentRanking,
			"purchases_ranking": purchasesRanking,
			"rules": map[string]interface{}{
				"frequent_customer_criteria": "Realizar al menos 1 compra por mes durante más de 3 meses consecutivos.",
				"frequent_points":            "1 punto de mes frecuente por cada mes consecutivo adicional tras superar los 3 meses.",
				"purchase_points":            "1 punto de compra por cada pedido realizado.",
				"benefits":                   "Acceso a descuentos exclusivos de socio + 1 número de rifa GRATIS por sorteo.",
			},
		})
	})

	// ==========================================
	// PRODUCTOS & CARGA MASIVA DE STOCK
	// ==========================================

	// Subir archivo (Imagen)
	uploadHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		err := r.ParseMultipartForm(10 << 20) // 10 MB limit
		if err != nil {
			http.Error(w, "Error al procesar el archivo", http.StatusBadRequest)
			return
		}

		file, handler, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "No se encontró el archivo", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Crear directorio si no existe
		_ = os.MkdirAll(filepath.Join("public", "uploads"), 0755)

		// Crear nombre único
		ext := filepath.Ext(handler.Filename)
		filename := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
		savePath := filepath.Join("public", "uploads", filename)

		dst, err := os.Create(savePath)
		if err != nil {
			http.Error(w, "Error al guardar el archivo", http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		if _, err := io.Copy(dst, file); err != nil {
			http.Error(w, "Error al escribir el archivo", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"url": "/uploads/" + filename,
		})
	}
	mux.HandleFunc("/api/upload", requireAdmin(uploadHandler))
	mux.HandleFunc("/api/admin/upload", requireAdmin(uploadHandler))

	// Subida y actualización de avatar de usuario (cliente o admin)
	userAvatarHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Método no permitido",
			})
			return
		}

		// 1. Limitar el tamaño de la petición a 2 MB en el servidor para proteger el almacenamiento
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			errMsg := "Error al procesar el formulario de subida."
			if strings.Contains(strings.ToLower(err.Error()), "too large") || strings.Contains(strings.ToLower(err.Error()), "request body too large") {
				errMsg = "La imagen supera el límite máximo permitido de 2 MB."
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   errMsg,
			})
			return
		}

		// 2. Identificar el usuario
		authUser, _ := getAuthenticatedUser(r)
		formEmail := strings.TrimSpace(r.FormValue("email"))
		formUserID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)

		var targetUserID int64
		var targetEmail string

		if authUser != nil {
			targetUserID = authUser.ID
			targetEmail = authUser.Email
		} else if formEmail != "" {
			targetEmail = formEmail
			targetUserID = formUserID
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Debes iniciar sesión para actualizar tu foto de perfil.",
			})
			return
		}

		// 3. Obtener el archivo
		file, handler, err := r.FormFile("avatar")
		if err != nil {
			file, handler, err = r.FormFile("file")
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "No se seleccionó ningún archivo de imagen.",
			})
			return
		}
		defer file.Close()

		// 4. Validar extensión
		rawExt := strings.ToLower(filepath.Ext(handler.Filename))
		allowedExts := map[string]bool{
			".jpg":  true,
			".jpeg": true,
			".png":  true,
			".webp": true,
			".gif":  true,
		}
		if !allowedExts[rawExt] {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Formato de imagen no válido. Formatos permitidos: JPG, PNG, WEBP, GIF.",
			})
			return
		}

		// 5. Validar cabecera binaria MIME
		headBuf := make([]byte, 512)
		n, _ := file.Read(headBuf)
		if seeker, ok := file.(io.Seeker); ok {
			_, _ = seeker.Seek(0, io.SeekStart)
		}
		mimeType := http.DetectContentType(headBuf[:n])
		if !strings.HasPrefix(mimeType, "image/") && !strings.Contains(mimeType, "octet-stream") {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "El archivo no es una imagen válida.",
			})
			return
		}

		// 6. Directorio para avatares
		avatarsDir := filepath.Join("public", "uploads", "avatars")
		_ = os.MkdirAll(avatarsDir, 0755)

		// 7. Nombre único sanitizado
		cleanExt := rawExt
		if cleanExt == "" {
			cleanExt = ".webp"
		}
		filename := fmt.Sprintf("avatar_%d_%d%s", time.Now().UnixNano(), targetUserID, cleanExt)
		savePath := filepath.Join(avatarsDir, filename)

		dst, err := os.Create(savePath)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Error al guardar la imagen en el servidor.",
			})
			return
		}
		defer dst.Close()

		if _, err := io.Copy(dst, file); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Error al escribir la imagen.",
			})
			return
		}

		avatarURL := "/uploads/avatars/" + filename

		// 8. Actualizar en memoria y limpiar avatar anterior si existía en uploads locales
		store.mu.Lock()
		var updatedUser User
		var userFound bool
		var oldAvatarToRemove string
		for i := range store.users {
			if (targetUserID != 0 && store.users[i].ID == targetUserID) || (targetEmail != "" && strings.EqualFold(store.users[i].Email, targetEmail)) {
				if strings.HasPrefix(store.users[i].AvatarURL, "/uploads/avatars/") {
					oldAvatarToRemove = strings.TrimPrefix(store.users[i].AvatarURL, "/")
				}
				store.users[i].AvatarURL = avatarURL
				updatedUser = store.users[i]
				userFound = true
				break
			}
		}
		store.mu.Unlock()

		// Limpiar archivo anterior para ahorrar espacio en disco del servidor
		if oldAvatarToRemove != "" {
			_ = os.Remove(filepath.Join("public", strings.TrimPrefix(oldAvatarToRemove, "uploads/avatars/")))
			_ = os.Remove(filepath.Join("public", oldAvatarToRemove))
		}

		if !userFound {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "Usuario no encontrado.",
			})
			return
		}

		// 9. Persistir en PostgreSQL
		pgSaveUser(updatedUser)

		userResp := updatedUser
		userResp.Password = ""

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":    true,
			"avatar_url": avatarURL,
			"user":       userResp,
			"message":    "Foto de perfil actualizada exitosamente.",
		})
	}
	mux.HandleFunc("/api/user/avatar", userAvatarHandler)


	// Obtener Tipos de Producto
	mux.HandleFunc("/api/product-types", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if dbActive && db != nil {
			rows, err := db.Query("SELECT id, name, code, COALESCE(is_active, true) FROM product_types ORDER BY id DESC")
			if err == nil {
				var types []ProductTypeModel
				for rows.Next() {
					var t ProductTypeModel
					if err := rows.Scan(&t.ID, &t.Name, &t.Code, &t.IsActive); err == nil {
						types = append(types, t)
					}
				}
				rows.Close()
				if len(types) > 0 {
					sort.Slice(types, func(i, j int) bool {
						return types[i].ID > types[j].ID
					})
					json.NewEncoder(w).Encode(types)
					return
				}
			}
		}

		store.mu.RLock()
		defer store.mu.RUnlock()
		typesCopy := make([]ProductTypeModel, len(store.productTypes))
		copy(typesCopy, store.productTypes)
		sort.Slice(typesCopy, func(i, j int) bool {
			return typesCopy[i].ID > typesCopy[j].ID
		})
		json.NewEncoder(w).Encode(typesCopy)
	})

	// Crear o Actualizar Tipo de Producto (Admin)
	mux.HandleFunc("/api/admin/product-types", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Metodo no permitido", http.StatusMethodNotAllowed)
			return
		}

		var pt ProductTypeModel
		if err := json.NewDecoder(r.Body).Decode(&pt); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pt.Name = strings.TrimSpace(pt.Name)
		pt.Code = strings.ToUpper(strings.TrimSpace(pt.Code))
		if pt.Name == "" || pt.Code == "" {
			http.Error(w, "Nombre y código son requeridos", http.StatusBadRequest)
			return
		}

		if pt.ID == 0 && !pt.IsActive {
			pt.IsActive = true
		}

		if dbActive && db != nil {
			if pt.ID == 0 {
				err := db.QueryRow("INSERT INTO product_types (name, code, is_active) VALUES ($1, $2, $3) RETURNING id", pt.Name, pt.Code, pt.IsActive).Scan(&pt.ID)
				if err != nil {
					http.Error(w, "Error creando tipo de producto: "+err.Error(), http.StatusInternalServerError)
					return
				}
			} else {
				_, err := db.Exec("UPDATE product_types SET name=$1, code=$2, is_active=$3 WHERE id=$4", pt.Name, pt.Code, pt.IsActive, pt.ID)
				if err != nil {
					http.Error(w, "Error actualizando tipo de producto: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}

		store.mu.Lock()
		updated := false
		for i := range store.productTypes {
			if store.productTypes[i].ID == pt.ID || strings.EqualFold(store.productTypes[i].Name, pt.Name) {
				store.productTypes[i] = pt
				updated = true
				break
			}
		}
		if !updated {
			if pt.ID == 0 {
				pt.ID = int64(len(store.productTypes) + 1)
			}
			store.productTypes = append([]ProductTypeModel{pt}, store.productTypes...)
		}
		store.mu.Unlock()

		json.NewEncoder(w).Encode(pt)
	}))

	// Toggle Activo en Tipos de Producto (Admin)
	mux.HandleFunc("/api/admin/product-types/toggle-active", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			TypeID   int64 `json:"type_id"`
			ID       int64 `json:"id"`
			IsActive *bool `json:"is_active,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		typeID := req.TypeID
		if typeID == 0 {
			typeID = req.ID
		}
		if typeID == 0 {
			http.Error(w, "ID de tipo de producto inválido", http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.productTypes {
			if store.productTypes[i].ID == typeID {
				if req.IsActive != nil {
					store.productTypes[i].IsActive = *req.IsActive
				} else {
					store.productTypes[i].IsActive = !store.productTypes[i].IsActive
				}
				newActive := store.productTypes[i].IsActive

				if dbActive && db != nil {
					_, err := db.Exec("UPDATE product_types SET is_active = $1 WHERE id = $2", newActive, typeID)
					if err != nil {
						log.Printf("⚠️ Error actualizando product_types is_active en Postgres: %v", err)
					}
				}

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":   true,
					"type_id":   typeID,
					"id":        typeID,
					"is_active": newActive,
					"message":   fmt.Sprintf("Tipo '%s' marcado como %s", store.productTypes[i].Name, map[bool]string{true: "ACTIVO", false: "INACTIVO"}[newActive]),
				})
				return
			}
		}

		http.Error(w, "Tipo de producto no encontrado", http.StatusNotFound)
	}))

	// Obtener Marcas
	mux.HandleFunc("/api/brands", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if dbActive && db != nil {
			rows, err := db.Query("SELECT id, name, code, COALESCE(carrusel, true), COALESCE(imagen, '') FROM brands ORDER BY id DESC")
			if err == nil {
				var brands []Brand
				for rows.Next() {
					var b Brand
					if err := rows.Scan(&b.ID, &b.Name, &b.Code, &b.Carrusel, &b.Imagen); err == nil {
						brands = append(brands, b)
					}
				}
				rows.Close()
				if len(brands) > 0 {
					sort.Slice(brands, func(i, j int) bool {
						return brands[i].ID > brands[j].ID
					})
					json.NewEncoder(w).Encode(brands)
					return
				}
			}
		}

		store.mu.RLock()
		defer store.mu.RUnlock()
		brandsCopy := make([]Brand, len(store.brands))
		copy(brandsCopy, store.brands)
		sort.Slice(brandsCopy, func(i, j int) bool {
			return brandsCopy[i].ID > brandsCopy[j].ID
		})
		json.NewEncoder(w).Encode(brandsCopy)
	})

	// Crear o Actualizar Marca (Admin)
	mux.HandleFunc("/api/admin/brands", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Metodo no permitido", http.StatusMethodNotAllowed)
			return
		}

		var b Brand
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if dbActive && db != nil {
			if b.ID == 0 {
				err := db.QueryRow("INSERT INTO brands (name, code, carrusel, imagen) VALUES ($1, $2, $3, $4) RETURNING id", b.Name, b.Code, b.Carrusel, b.Imagen).Scan(&b.ID)
				if err != nil {
					http.Error(w, "Error creando marca: "+err.Error(), http.StatusInternalServerError)
					return
				}
			} else {
				_, err := db.Exec("UPDATE brands SET name=$1, code=$2, carrusel=$3, imagen=$4 WHERE id=$5", b.Name, b.Code, b.Carrusel, b.Imagen, b.ID)
				if err != nil {
					http.Error(w, "Error actualizando marca: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}

		store.mu.Lock()
		updated := false
		for i := range store.brands {
			if store.brands[i].ID == b.ID || strings.EqualFold(store.brands[i].Name, b.Name) {
				store.brands[i] = b
				updated = true
				break
			}
		}
		if !updated {
			if b.ID == 0 {
				b.ID = int64(len(store.brands) + 1)
			}
			store.brands = append(store.brands, b)
		}
		store.mu.Unlock()

		json.NewEncoder(w).Encode(b)
	}))

	// Toggle Carrusel en Marcas (Admin)
	mux.HandleFunc("/api/admin/brands/toggle-carrusel", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			BrandID  int64 `json:"brand_id"`
			ID       int64 `json:"id"`
			Carrusel *bool `json:"carrusel,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		brandID := req.BrandID
		if brandID == 0 {
			brandID = req.ID
		}
		if brandID == 0 {
			http.Error(w, "ID de marca inválido", http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.brands {
			if store.brands[i].ID == brandID {
				if req.Carrusel != nil {
					store.brands[i].Carrusel = *req.Carrusel
				} else {
					store.brands[i].Carrusel = !store.brands[i].Carrusel
				}
				newStatus := store.brands[i].Carrusel

				if dbActive && db != nil {
					_, err := db.Exec("UPDATE brands SET carrusel = $1 WHERE id = $2", newStatus, brandID)
					if err != nil {
						log.Printf("⚠️ Error actualizando brands carrusel en Postgres: %v", err)
					}
				}

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":  true,
					"brand_id": brandID,
					"id":       brandID,
					"carrusel": newStatus,
					"message":  fmt.Sprintf("Marca '%s' carrusel: %v", store.brands[i].Name, newStatus),
				})
				return
			}
		}

		http.Error(w, "Marca no encontrada", http.StatusNotFound)
	}))

	// Obtener Productos (con filtros)
	mux.HandleFunc("/api/products", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		store.mu.RLock()
		defer store.mu.RUnlock()

		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		vendor := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("vendor")))
		status := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("status")))
		scale := strings.TrimSpace(r.URL.Query().Get("scale"))
		prodType := strings.TrimSpace(r.URL.Query().Get("type"))
		onlyActive := r.URL.Query().Get("active") != "false"

		var filtered []Product
		for _, p := range store.products {
			if onlyActive && !p.IsActive {
				continue
			}
			if vendor != "" && !strings.Contains(strings.ToLower(p.Vendor), vendor) {
				continue
			}
			if status != "" && status != "ALL" && p.Status != status {
				continue
			}
			if scale != "" && scale != "ALL" && !strings.EqualFold(p.Scale, scale) {
				continue
			}
			if prodType != "" && prodType != "ALL" && !strings.EqualFold(p.ProductType, prodType) {
				continue
			}
			if q != "" {
				text := strings.ToLower(fmt.Sprintf("%s %s %s %s %s", p.Title, p.Vendor, p.ProductType, p.Scale, p.ApparelSize))
				if !strings.Contains(text, q) {
					continue
				}
			}
			filtered = append(filtered, p)
		}

		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].ID > filtered[j].ID
		})

		json.NewEncoder(w).Encode(map[string]interface{}{
			"count":    len(filtered),
			"products": filtered,
		})
	})

	// Carga Masiva de Artículos por Excel (Batch Import con Validaciones)
	mux.HandleFunc("/api/admin/products/bulk-import", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Products []BulkImportProductItem `json:"products"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Error decodificando datos: "+err.Error(), http.StatusBadRequest)
			return
		}

		if len(req.Products) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   "El archivo Excel no contiene artículos válidos para importar.",
				"message": "El archivo Excel no contiene artículos válidos para importar.",
			})
			return
		}

		// 1. Control & Validación exhaustiva fila por fila
		var validationErrors []string
		for i, item := range req.Products {
			rowNum := i + 2 // En Excel la fila 1 son los encabezados
			title := strings.TrimSpace(item.Title)
			prodType := strings.TrimSpace(item.ProductType)
			vendor := strings.TrimSpace(item.Vendor)
			scale := strings.TrimSpace(item.Scale)
			size := strings.TrimSpace(item.ApparelSize)

			if title == "" {
				validationErrors = append(validationErrors, fmt.Sprintf("Fila %d: El campo 'Título / Nombre' es obligatorio.", rowNum))
			}
			if prodType == "" {
				validationErrors = append(validationErrors, fmt.Sprintf("Fila %d: El campo 'Tipo de Artículo' es obligatorio (ej: Autito, Remera, Sticker).", rowNum))
			}
			if vendor == "" {
				validationErrors = append(validationErrors, fmt.Sprintf("Fila %d: El campo 'Marca' es obligatorio.", rowNum))
			}

			// Validaciones condicionales según tipo
			typeLower := strings.ToLower(prodType)
			if (strings.Contains(typeLower, "autito") || strings.Contains(typeLower, "diecast") || strings.Contains(typeLower, "auto")) && scale == "" {
				validationErrors = append(validationErrors, fmt.Sprintf("Fila %d: Para artículos de tipo '%s' es obligatoria la 'Escala' (ej: 1:64, 1:43, 1:18).", rowNum, prodType))
			}
			if (strings.Contains(typeLower, "remera") || strings.Contains(typeLower, "apparel") || strings.Contains(typeLower, "ropa") || strings.Contains(typeLower, "indumentaria") || strings.Contains(typeLower, "buzo")) && size == "" {
				validationErrors = append(validationErrors, fmt.Sprintf("Fila %d: Para artículos de tipo '%s' es obligatorio el 'Talle' (ej: S, M, L, XL, XXL).", rowNum, prodType))
			}

			if item.PriceARS <= 0 {
				validationErrors = append(validationErrors, fmt.Sprintf("Fila %d: El 'Precio ARS' debe ser un número entero mayor a 0 (recibido: %d).", rowNum, item.PriceARS))
			}
			if item.StockQuantity < 0 {
				validationErrors = append(validationErrors, fmt.Sprintf("Fila %d: La cantidad de 'Stock' no puede ser negativa (recibido: %d).", rowNum, item.StockQuantity))
			}
		}

		// Si se encontraron errores, reportarlos todos juntos
		if len(validationErrors) > 0 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success":      false,
				"total_errors": len(validationErrors),
				"errors":       validationErrors,
				"message":      fmt.Sprintf("Se encontraron %d error(es) de validación en el archivo Excel. Ningún artículo fue ingresado a la base de datos.", len(validationErrors)),
			})
			return
		}

		// 2. Si no hay errores, proceder con la inserción por lotes con IDs autonuméricos
		var maxID int64 = 0
		if dbActive && db != nil {
			_ = db.QueryRow("SELECT COALESCE(MAX(id), 0) FROM products WHERE id < 1000000000").Scan(&maxID)
		}
		store.mu.Lock()
		for _, ep := range store.products {
			if ep.ID > maxID && ep.ID < 1000000000 {
				maxID = ep.ID
			}
		}

		var importedProducts []Product
		for _, item := range req.Products {
			maxID++
			prodID := maxID

			vendorName := strings.TrimSpace(item.Vendor)
			typeName := strings.TrimSpace(item.ProductType)

			// Buscar o crear marca
			var brandID *int64
			for _, b := range store.brands {
				if strings.EqualFold(b.Name, vendorName) {
					bID := b.ID
					brandID = &bID
					vendorName = b.Name
					break
				}
			}
			if brandID == nil {
				newBID := int64(len(store.brands) + 1)
				code := strings.ToUpper(strings.ReplaceAll(vendorName, " ", "_"))
				newBrand := Brand{ID: newBID, Name: vendorName, Code: code, Carrusel: true}
				store.brands = append([]Brand{newBrand}, store.brands...)
				brandID = &newBID
				if dbActive && db != nil {
					_, _ = db.Exec("INSERT INTO brands (id, name, code, carrusel) VALUES ($1, $2, $3, true) ON CONFLICT (name) DO NOTHING", newBID, vendorName, code)
				}
			}

			// Buscar o crear tipo de artículo
			var typeID *int64
			for _, t := range store.productTypes {
				if strings.EqualFold(t.Name, typeName) {
					tID := t.ID
					typeID = &tID
					typeName = t.Name
					break
				}
			}
			if typeID == nil {
				newTID := int64(len(store.productTypes) + 1)
				code := strings.ToUpper(strings.ReplaceAll(typeName, " ", "_"))
				newType := ProductTypeModel{ID: newTID, Name: typeName, Code: code, IsActive: true}
				store.productTypes = append([]ProductTypeModel{newType}, store.productTypes...)
				typeID = &newTID
				if dbActive && db != nil {
					_, _ = db.Exec("INSERT INTO product_types (id, name, code, is_active) VALUES ($1, $2, $3, true) ON CONFLICT (name) DO NOTHING", newTID, typeName, code)
				}
			}

			// Galería de imágenes
			var gallery []string
			if strings.TrimSpace(item.GalleryImages) != "" {
				parts := strings.Split(item.GalleryImages, ",")
				for _, p := range parts {
					if trimmed := strings.TrimSpace(p); trimmed != "" {
						gallery = append(gallery, trimmed)
					}
				}
			}
			if len(gallery) == 0 {
				gallery = []string{"https://images.unsplash.com/photo-1581235720704-06d3acfcb36f?auto=format&fit=crop&w=600&q=80"}
			}

			title := strings.TrimSpace(item.Title)
			handle := strings.ToLower(strings.ReplaceAll(title, " ", "-"))
			priceUSD := strings.TrimSpace(item.PriceUSD)
			if priceUSD == "" {
				priceUSD = fmt.Sprintf("%.2f", float64(item.PriceARS)/1350.0)
			}

			status := strings.ToUpper(strings.TrimSpace(item.Status))
			if item.StockQuantity == 0 {
				status = "AGOTADO"
			} else if status == "" {
				status = "STOCK"
			}

			isActive := true
			actStr := strings.ToUpper(strings.TrimSpace(item.Activo))
			if actStr == "NO" || actStr == "FALSE" || actStr == "0" {
				isActive = false
			}

			hasChase := false
			chaseStr := strings.ToUpper(strings.TrimSpace(item.HasChaseChance))
			if chaseStr == "SI" || chaseStr == "SÍ" || chaseStr == "TRUE" || chaseStr == "1" {
				hasChase = true
			}

			inSlider := false
			inSlStr := strings.ToUpper(strings.TrimSpace(item.InSlider))
			if inSlStr == "SI" || inSlStr == "SÍ" || inSlStr == "TRUE" || inSlStr == "1" {
				inSlider = true
			}

			internalCode := strings.TrimSpace(item.InternalCode)
			if internalCode == "" {
				internalCode = fmt.Sprintf("SKU-%04d", prodID)
			}

			prod := Product{
				ID:             prodID,
				InternalCode:   internalCode,
				Title:          title,
				Handle:         handle,
				BodyHTML:       strings.TrimSpace(item.Description),
				Vendor:         vendorName,
				BrandID:        brandID,
				ProductType:    typeName,
				ProductTypeID:  typeID,
				Scale:          strings.TrimSpace(item.Scale),
				ApparelSize:    strings.TrimSpace(item.ApparelSize),
				PriceARS:       item.PriceARS,
				PriceUSD:       priceUSD,
				StockQuantity:  item.StockQuantity,
				Status:         status,
				IsActive:       isActive,
				HasChaseChance: hasChase,
				InSlider:       inSlider,
				GalleryImages:  gallery,
				Images:         []ProductImage{{ID: 1, Position: 1, Src: gallery[0]}},
				ShortVideoURL:  strings.TrimSpace(item.ShortVideoURL),
			}

			store.products = append([]Product{prod}, store.products...)
			pgSaveProduct(prod)
			importedProducts = append(importedProducts, prod)
		}
		store.mu.Unlock()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":        true,
			"imported_count": len(importedProducts),
			"products":       importedProducts,
			"message":        fmt.Sprintf("¡Carga exitosa! Se importaron %d artículos correctamente.", len(importedProducts)),
		})
	}))

	// Crear Nuevo Artículo (Admin)
	mux.HandleFunc("/api/admin/products", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var p Product
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if dbActive && db != nil {
			if p.BrandID != nil && *p.BrandID > 0 {
				_ = db.QueryRow("SELECT name FROM brands WHERE id = $1", *p.BrandID).Scan(&p.Vendor)
			}
			if p.ProductTypeID != nil && *p.ProductTypeID > 0 {
				_ = db.QueryRow("SELECT name FROM product_types WHERE id = $1", *p.ProductTypeID).Scan(&p.ProductType)
			}
		}

		// Validaciones requeridas según la especificación:
		// Si es "Autito" -> pide escala
		// Si es "Remera" -> pide talle
		// Si es "Sticker" -> ninguna
		if strings.EqualFold(p.ProductType, "Autito") && p.Scale == "" {
			http.Error(w, "Para artículos tipo 'Autito' es obligatorio especificar la Escala (ej: 1:64, 1:18, 1:43)", http.StatusBadRequest)
			return
		}
		if strings.EqualFold(p.ProductType, "Remera") && p.ApparelSize == "" {
			http.Error(w, "Para artículos tipo 'Remera' es obligatorio especificar el Talle (ej: S, M, L, XL, XXL)", http.StatusBadRequest)
			return
		}

		// Asignación de ID autonumérico de 1 en 1
		if p.ID == 0 {
			var maxID int64 = 0
			if dbActive && db != nil {
				_ = db.QueryRow("SELECT COALESCE(MAX(id), 0) FROM products WHERE id < 1000000000").Scan(&maxID)
			}
			store.mu.RLock()
			for _, ep := range store.products {
				if ep.ID > maxID && ep.ID < 1000000000 {
					maxID = ep.ID
				}
			}
			store.mu.RUnlock()
			p.ID = maxID + 1
		}

		if p.Handle == "" {
			p.Handle = strings.ToLower(strings.ReplaceAll(p.Title, " ", "-"))
		}
		if p.PriceUSD == "" {
			p.PriceUSD = fmt.Sprintf("%.2f", float64(p.PriceARS)/1350.0)
		}
		if p.StockQuantity == 0 {
			p.Status = "AGOTADO"
		} else if p.Status == "" {
			p.Status = "STOCK"
		}

		store.mu.Lock()
		store.products = append([]Product{p}, store.products...)
		store.mu.Unlock()
		pgSaveProduct(p)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"product": p,
			"message": "Artículo agregado correctamente al catálogo",
		})
	}))

	// Toggle Activo en Catálogo / Artículos
	mux.HandleFunc("/api/admin/products/toggle-active", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ProductID int64 `json:"product_id"`
			ID        int64 `json:"id"`
			IsActive  *bool `json:"is_active,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		prodID := req.ProductID
		if prodID == 0 {
			prodID = req.ID
		}
		if prodID == 0 {
			http.Error(w, "ID de producto inválido", http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.products {
			if store.products[i].ID == prodID {
				if req.IsActive != nil {
					store.products[i].IsActive = *req.IsActive
				} else {
					store.products[i].IsActive = !store.products[i].IsActive
				}
				newActive := store.products[i].IsActive
				pgSaveProduct(store.products[i])

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":    true,
					"product_id": req.ProductID,
					"is_active":  newActive,
					"message":    fmt.Sprintf("Artículo '%s' marcado como %s", store.products[i].Title, map[bool]string{true: "ACTIVO", false: "INACTIVO"}[newActive]),
				})
				return
			}
		}
		http.Error(w, "Artículo no encontrado", http.StatusNotFound)
	}))

	// ==========================================
	// CONFIGURACIÓN Y GESTIÓN DE SLIDER / CARRUSEL
	// ==========================================

	// Obtener Configuración del Slider (Público y Admin)
	mux.HandleFunc("/api/slider-config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		store.mu.RLock()
		defer store.mu.RUnlock()

		var selectedCount int
		for _, p := range store.products {
			if p.InSlider && p.IsActive {
				selectedCount++
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"max_items":      store.sliderConfig.MaxItems,
			"active":         store.sliderConfig.Active,
			"selected_count": selectedCount,
		})
	})

	// Guardar Configuración del Slider (Admin)
	mux.HandleFunc("/api/admin/slider-config", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			MaxItems int   `json:"max_items"`
			Active   *bool `json:"active,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.MaxItems <= 0 {
			req.MaxItems = 5
		}

		store.mu.Lock()
		store.sliderConfig.MaxItems = req.MaxItems
		if req.Active != nil {
			store.sliderConfig.Active = *req.Active
		}
		store.mu.Unlock()

		if dbActive && db != nil {
			_, _ = db.Exec("INSERT INTO site_settings (key, value) VALUES ('slider_max_items', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", strconv.Itoa(req.MaxItems))
			_, _ = db.Exec("INSERT INTO site_settings (key, value) VALUES ('slider_active', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", fmt.Sprintf("%t", store.sliderConfig.Active))
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"max_items": store.sliderConfig.MaxItems,
			"active":    store.sliderConfig.Active,
			"message":   fmt.Sprintf("Configuración guardada: Máximo %d artículos en el slider.", store.sliderConfig.MaxItems),
		})
	}))

	// Toggle Producto en Slider (Admin)
	mux.HandleFunc("/api/admin/products/toggle-slider", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ProductID int64 `json:"product_id"`
			ID        int64 `json:"id"`
			InSlider  *bool `json:"in_slider,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		prodID := req.ProductID
		if prodID == 0 {
			prodID = req.ID
		}
		if prodID == 0 {
			http.Error(w, "ID de producto inválido", http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		var matched *Product
		for i := range store.products {
			if store.products[i].ID == prodID {
				if req.InSlider != nil {
					store.products[i].InSlider = *req.InSlider
				} else {
					store.products[i].InSlider = !store.products[i].InSlider
				}
				matched = &store.products[i]
				pgSaveProduct(store.products[i])
				break
			}
		}

		if matched == nil {
			http.Error(w, "Artículo no encontrado", http.StatusNotFound)
			return
		}

		var totalInSlider int
		for _, p := range store.products {
			if p.InSlider && p.IsActive {
				totalInSlider++
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":         true,
			"product_id":      req.ProductID,
			"in_slider":       matched.InSlider,
			"total_in_slider": totalInSlider,
			"max_items":       store.sliderConfig.MaxItems,
			"message":         fmt.Sprintf("Artículo '%s' %s del slider", matched.Title, map[bool]string{true: "agregado al", false: "removido del"}[matched.InSlider]),
		})
	}))

	// Selección masiva de artículos para el slider (Admin)
	mux.HandleFunc("/api/admin/slider-products/bulk-select", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ProductIDs []int64 `json:"product_ids"`
			MaxItems   int     `json:"max_items,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		idMap := make(map[int64]bool)
		for _, id := range req.ProductIDs {
			idMap[id] = true
		}

		store.mu.Lock()
		if req.MaxItems > 0 {
			store.sliderConfig.MaxItems = req.MaxItems
			if dbActive && db != nil {
				_, _ = db.Exec("INSERT INTO site_settings (key, value) VALUES ('slider_max_items', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", strconv.Itoa(req.MaxItems))
			}
		}

		var updatedCount int
		for i := range store.products {
			shouldBeInSlider := idMap[store.products[i].ID]
			if store.products[i].InSlider != shouldBeInSlider {
				store.products[i].InSlider = shouldBeInSlider
				pgSaveProduct(store.products[i])
			}
			if shouldBeInSlider && store.products[i].IsActive {
				updatedCount++
			}
		}
		store.mu.Unlock()

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":        true,
			"selected_count": updatedCount,
			"max_items":      store.sliderConfig.MaxItems,
			"message":        fmt.Sprintf("Slider actualizado: %d artículo(s) seleccionados. Límite: %d.", updatedCount, store.sliderConfig.MaxItems),
		})
	}))

	// Obtener Artículos para el Slider de la Tienda (Público)
	mux.HandleFunc("/api/slider-products", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		store.mu.RLock()
		defer store.mu.RUnlock()

		maxItems := store.sliderConfig.MaxItems
		if maxItems <= 0 {
			maxItems = 5
		}

		var sliderProducts []Product
		for _, p := range store.products {
			if p.InSlider && p.IsActive {
				sliderProducts = append(sliderProducts, p)
				if len(sliderProducts) >= maxItems {
					break
				}
			}
		}

		// Fallback si todavía no se seleccionó ninguno manualmente
		if len(sliderProducts) == 0 {
			for _, p := range store.products {
				if p.IsActive && (p.HasChaseChance || p.Status == "PRE_VENTA" || p.PriceARS > 20000 || p.ProductType == "Remera") {
					sliderProducts = append(sliderProducts, p)
					if len(sliderProducts) >= maxItems {
						break
					}
				}
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"max_items": store.sliderConfig.MaxItems,
			"active":    store.sliderConfig.Active,
			"products":  sliderProducts,
		})
	})

	// Carga Masiva de Stock
	mux.HandleFunc("/api/admin/stock/bulk-add", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			ProductID   int64 `json:"product_id"`
			QuantityAdd int   `json:"quantity_add"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.QuantityAdd <= 0 {
			http.Error(w, "La cantidad a sumar debe ser mayor a 0", http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.products {
			if store.products[i].ID == req.ProductID {
				prev := store.products[i].StockQuantity
				store.products[i].StockQuantity += req.QuantityAdd
				if store.products[i].StockQuantity > 0 && store.products[i].Status == "AGOTADO" {
					store.products[i].Status = "STOCK"
				}
				pgSaveProduct(store.products[i])

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":        true,
					"product_id":     req.ProductID,
					"product_title":  store.products[i].Title,
					"previous_stock": prev,
					"new_stock":      store.products[i].StockQuantity,
					"status":         store.products[i].Status,
					"message":        fmt.Sprintf("Se sumaron %d unidades al stock. Stock actual: %d", req.QuantityAdd, store.products[i].StockQuantity),
				})
				return
			}
		}

		http.Error(w, "Producto no encontrado", http.StatusNotFound)
	}))

	// ==========================================
	// RIFAS & SORTEOS
	// ==========================================

	// Listar Rifas
	mux.HandleFunc("/api/raffles", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		store.mu.RLock()
		defer store.mu.RUnlock()

		type RaffleResponseItem struct {
			Raffle
			TotalNumbers   int     `json:"total_numbers"`
			SoldNumbers    int     `json:"sold_numbers"`
			SoldPercentage float64 `json:"sold_percentage"`
			TakenNumbers   []int   `json:"taken_numbers"`
		}

		var response []RaffleResponseItem
		for _, raf := range store.raffles {
			total := (raf.MaxNumber - raf.MinNumber) + 1
			sold := len(raf.Tickets)
			var taken []int
			for num := range raf.Tickets {
				taken = append(taken, num)
			}

			pct := 0.0
			if total > 0 {
				pct = (float64(sold) / float64(total)) * 100.0
			}

			response = append(response, RaffleResponseItem{
				Raffle:         raf,
				TotalNumbers:   total,
				SoldNumbers:    sold,
				SoldPercentage: pct,
				TakenNumbers:   taken,
			})
		}

		sort.Slice(response, func(i, j int) bool {
			return response[i].ID > response[j].ID
		})

		json.NewEncoder(w).Encode(map[string]interface{}{
			"raffles": response,
		})
	})

	// Crear Nueva Rifa (Admin)
	mux.HandleFunc("/api/admin/raffles", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			RaffleNumber     string   `json:"raffle_number"`
			Title            string   `json:"title"`
			PrizeDescription string   `json:"prize_description"`
			PrizeImages      []string `json:"prize_images"`
			StartDatetime    string   `json:"start_datetime"`
			EndDatetime      string   `json:"end_datetime"`
			DrawDatetime     string   `json:"draw_datetime"`
			MinNumber        int      `json:"min_number"`
			MaxNumber        int      `json:"max_number"`
			TicketPriceARS   int      `json:"ticket_price_ars"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		req.Title = strings.TrimSpace(req.Title)
		req.RaffleNumber = strings.TrimSpace(req.RaffleNumber)
		req.StartDatetime = strings.TrimSpace(req.StartDatetime)
		req.EndDatetime = strings.TrimSpace(req.EndDatetime)
		req.DrawDatetime = strings.TrimSpace(req.DrawDatetime)

		if req.Title == "" || req.RaffleNumber == "" {
			http.Error(w, "Título y número de sorteo son requeridos", http.StatusBadRequest)
			return
		}

		if req.StartDatetime == "" || req.EndDatetime == "" || req.DrawDatetime == "" {
			http.Error(w, "Las fechas de inicio, fin y sorteo oficial son obligatorias", http.StatusBadRequest)
			return
		}

		if req.EndDatetime < req.StartDatetime {
			http.Error(w, "La fecha de fin no puede ser anterior a la fecha de inicio", http.StatusBadRequest)
			return
		}

		if req.DrawDatetime < req.EndDatetime {
			http.Error(w, "La fecha del sorteo oficial no puede ser anterior a la finalización de venta de boletos", http.StatusBadRequest)
			return
		}

		var maxID int64 = 0
		if dbActive && db != nil {
			_ = db.QueryRow("SELECT COALESCE(MAX(id), 0) FROM raffles WHERE id < 1000000000").Scan(&maxID)
		}
		store.mu.RLock()
		for _, er := range store.raffles {
			if er.ID > maxID && er.ID < 1000000000 {
				maxID = er.ID
			}
		}
		store.mu.RUnlock()

		newRaffle := Raffle{
			ID:               maxID + 1,
			RaffleNumber:     req.RaffleNumber,
			Title:            req.Title,
			PrizeDescription: req.PrizeDescription,
			PrizeImages:      req.PrizeImages,
			StartDatetime:    req.StartDatetime,
			EndDatetime:      req.EndDatetime,
			DrawDatetime:     req.DrawDatetime,
			MinNumber:        req.MinNumber,
			MaxNumber:        req.MaxNumber,
			TicketPriceARS:   req.TicketPriceARS,
			Status:           "ACTIVA",
			Tickets:          make(map[int]RaffleTicket),
		}

		store.mu.Lock()
		store.raffles = append([]Raffle{newRaffle}, store.raffles...)
		store.mu.Unlock()
		pgSaveRaffle(newRaffle)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"raffle":  newRaffle,
			"message": "Rifa creada y abierta al público",
		})
	}))

	// Datos en Vivo para Ruleta / Sorteo en Vivo (Streams & OBS)
	mux.HandleFunc("/api/admin/raffles/live-data", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		store.mu.RLock()
		defer store.mu.RUnlock()

		raffleIDStr := r.URL.Query().Get("id")
		var target *Raffle
		if raffleIDStr != "" {
			id, _ := strconv.ParseInt(raffleIDStr, 10, 64)
			for i := range store.raffles {
				if store.raffles[i].ID == id {
					target = &store.raffles[i]
					break
				}
			}
		}
		if target == nil && len(store.raffles) > 0 {
			target = &store.raffles[0] // Primera por defecto
		}

		if target == nil {
			http.Error(w, "No hay rifas disponibles", http.StatusNotFound)
			return
		}

		type LiveParticipant struct {
			Number        int    `json:"number"`
			CustomerName  string `json:"customer_name"`
			CustomerEmail string `json:"customer_email"`
			IsFree        bool   `json:"is_free"`
		}

		var participants []LiveParticipant
		for num, t := range target.Tickets {
			name := t.CustomerName
			if name == "" {
				name = "Participante #" + strconv.Itoa(num)
			}
			participants = append(participants, LiveParticipant{
				Number:        num,
				CustomerName:  name,
				CustomerEmail: t.CustomerEmail,
				IsFree:        t.IsFreeTicket,
			})
		}

		sort.Slice(participants, func(i, j int) bool {
			return participants[i].Number < participants[j].Number
		})

		type SimpleRaffleInfo struct {
			ID           int64  `json:"id"`
			RaffleNumber string `json:"raffle_number"`
			Title        string `json:"title"`
			Status       string `json:"status"`
			TicketsCount int    `json:"tickets_count"`
		}
		var raffleList []SimpleRaffleInfo
		for _, raf := range store.raffles {
			raffleList = append(raffleList, SimpleRaffleInfo{
				ID:           raf.ID,
				RaffleNumber: raf.RaffleNumber,
				Title:        raf.Title,
				Status:       raf.Status,
				TicketsCount: len(raf.Tickets),
			})
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"raffle":       target,
			"participants": participants,
			"all_raffles":  raffleList,
		})
	}))

	// Ejecutar Sorteo y Registrar Ganador Oficial
	mux.HandleFunc("/api/admin/raffles/draw", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			RaffleID      int64 `json:"raffle_id"`
			WinningNumber *int  `json:"winning_number,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		var target *Raffle
		for i := range store.raffles {
			if store.raffles[i].ID == req.RaffleID {
				target = &store.raffles[i]
				break
			}
		}

		if target == nil {
			http.Error(w, "Rifa no encontrada", http.StatusNotFound)
			return
		}

		if len(target.Tickets) == 0 {
			http.Error(w, "No hay números vendidos para esta rifa", http.StatusBadRequest)
			return
		}

		var chosenNumber int
		if req.WinningNumber != nil {
			chosenNumber = *req.WinningNumber
		} else {
			// Sorteo aleatorio entre los números asignados/vendidos
			var pool []int
			for num := range target.Tickets {
				pool = append(pool, num)
			}
			chosenNumber = pool[rand.Intn(len(pool))]
		}

		ticket, exists := target.Tickets[chosenNumber]
		if !exists {
			http.Error(w, fmt.Sprintf("El número %d no fue adquirido en este sorteo", chosenNumber), http.StatusBadRequest)
			return
		}

		target.Status = "SORTEADA"
		target.WinnerNumber = &chosenNumber

		pgSaveRaffle(*target)

		log.Printf("🎉 [Sorteo Oficial KIDO] ¡Rifa %s SORTEADA! Ganador: Número #%d - %s (%s)", target.RaffleNumber, chosenNumber, ticket.CustomerName, ticket.CustomerEmail)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":        true,
			"raffle_id":      target.ID,
			"winning_number": chosenNumber,
			"winner": map[string]interface{}{
				"number":         chosenNumber,
				"name":           ticket.CustomerName,
				"email":          ticket.CustomerEmail,
				"is_free":        ticket.IsFreeTicket,
				"mercadopago_id": ticket.MercadoPagoID,
			},
			"raffle": target,
		})
	}))

	// Reiniciar Rifa a Estado Activa (Para Pruebas y Streams)
	mux.HandleFunc("/api/admin/raffles/reset", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			RaffleID int64 `json:"raffle_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.raffles {
			if store.raffles[i].ID == req.RaffleID {
				store.raffles[i].Status = "ACTIVA"
				store.raffles[i].WinnerNumber = nil
				pgSaveRaffle(store.raffles[i])

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"message": "Rifa restablecida a estado ACTIVA para sorteo",
					"raffle":  store.raffles[i],
				})
				return
			}
		}
		http.Error(w, "Rifa no encontrada", http.StatusNotFound)
	}))

	// Toggle Estado de Rifa (ACTIVA <-> PAUSADA)
	mux.HandleFunc("/api/admin/raffles/toggle-status", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			RaffleID int64 `json:"raffle_id"`
			ID       int64 `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		raffleID := req.RaffleID
		if raffleID == 0 {
			raffleID = req.ID
		}
		if raffleID == 0 {
			http.Error(w, "ID de rifa inválido", http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.raffles {
			if store.raffles[i].ID == raffleID {
				if store.raffles[i].Status == "ACTIVA" {
					store.raffles[i].Status = "PAUSADA"
				} else {
					store.raffles[i].Status = "ACTIVA"
				}
				newStatus := store.raffles[i].Status
				pgSaveRaffle(store.raffles[i])

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":   true,
					"raffle_id": req.RaffleID,
					"status":    newStatus,
					"message":   fmt.Sprintf("Rifa '%s' estado: %s", store.raffles[i].Title, newStatus),
				})
				return
			}
		}
		http.Error(w, "Rifa no encontrada", http.StatusNotFound)
	}))

	// Comprar Número de Rifa (o reclamar número gratis si es cliente frecuente)
	mux.HandleFunc("/api/raffles/buy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			RaffleID      int64  `json:"raffle_id"`
			Number        int    `json:"number"`
			CustomerID    int64  `json:"customer_id"`
			CustomerEmail string `json:"customer_email"`
			CustomerName  string `json:"customer_name"`
			ClaimFree     bool   `json:"claim_free"` // Si es el número gratis de cliente frecuente
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		var targetRaffle *Raffle
		for i := range store.raffles {
			if store.raffles[i].ID == req.RaffleID {
				targetRaffle = &store.raffles[i]
				break
			}
		}

		if targetRaffle == nil {
			http.Error(w, "Rifa no encontrada", http.StatusNotFound)
			return
		}

		if req.Number < targetRaffle.MinNumber || req.Number > targetRaffle.MaxNumber {
			http.Error(w, "El número está fuera del rango de la rifa", http.StatusBadRequest)
			return
		}

		if _, exists := targetRaffle.Tickets[req.Number]; exists {
			http.Error(w, fmt.Sprintf("El número %d ya fue vendido o reservado", req.Number), http.StatusConflict)
			return
		}

		isFree := false
		if req.ClaimFree {
			// Validar si el cliente realmente califica como cliente frecuente
			for i := range store.users {
				if store.users[i].ID == req.CustomerID || strings.EqualFold(store.users[i].Email, req.CustomerEmail) {
					if store.users[i].IsFrequentCustomer {
						if store.users[i].HasClaimedFreeRaffle {
							http.Error(w, "Ya utilizaste tu número gratuito de cliente frecuente para este sorteo", http.StatusBadRequest)
							return
						}
						isFree = true
						store.users[i].HasClaimedFreeRaffle = true
					}
					break
				}
			}
		}

		mpID := fmt.Sprintf("MP-RIFA-%d-%d", targetRaffle.ID, req.Number)
		if isFree {
			mpID = "BENEFICIO-CLIENTE-FRECUENTE"
		}

		targetRaffle.Tickets[req.Number] = RaffleTicket{
			Number:        req.Number,
			CustomerID:    req.CustomerID,
			CustomerEmail: req.CustomerEmail,
			CustomerName:  req.CustomerName,
			IsFreeTicket:  isFree,
			MercadoPagoID: mpID,
			PurchasedAt:   time.Now(),
		}
		pgSaveRaffleTicket(targetRaffle.ID, targetRaffle.Tickets[req.Number])

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":        true,
			"ticket_number":  req.Number,
			"is_free":        isFree,
			"mercadopago_id": mpID,
			"message":        fmt.Sprintf("¡Número %d asignado con éxito para %s!", req.Number, req.CustomerName),
		})
	})

	// ==========================================
	// PEDIDOS, PAGOS PARCIALES & MERCADO PAGO
	// ==========================================

	// Crear Pedido (Pago Total o Pago Parcial / Seña)
	mux.HandleFunc("/api/orders", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			// Obtener órdenes (para admin o usuario)
			store.mu.RLock()
			defer store.mu.RUnlock()
			userEmail := r.URL.Query().Get("email")
			if userEmail != "" {
				var userOrders []Order
				for _, o := range store.orders {
					if strings.EqualFold(o.CustomerEmail, userEmail) {
						userOrders = append(userOrders, o)
					}
				}
				sort.Slice(userOrders, func(i, j int) bool {
					return userOrders[i].ID > userOrders[j].ID
				})
				json.NewEncoder(w).Encode(userOrders)
				return
			}
			ordersCopy := make([]Order, len(store.orders))
			copy(ordersCopy, store.orders)
			sort.Slice(ordersCopy, func(i, j int) bool {
				return ordersCopy[i].ID > ordersCopy[j].ID
			})
			json.NewEncoder(w).Encode(ordersCopy)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			CustomerID         int64       `json:"customer_id"`
			CustomerEmail      string      `json:"customer_email"`
			CustomerName       string      `json:"customer_name"`
			Items              []OrderItem `json:"items"`
			PayPartial         bool        `json:"pay_partial"`          // true si paga seña / reserva
			PartialAmount      int         `json:"partial_amount"`       // monto pagado ahora
			OrderType          string      `json:"order_type"`           // "VENTA_DIRECTA" o "PRE_VENTA_CON_SEÑA"
			ShippingMethod     string      `json:"shipping_method"`      // Método de entrega seleccionado
			ShippingCostARS    int         `json:"shipping_cost_ars"`    // Costo calculado de envío
			ShippingPostalCode string      `json:"shipping_postal_code"` // Código postal
			ShippingAddress    string      `json:"shipping_address"`     // Dirección completa de entrega
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		itemsSubtotal := 0
		for _, it := range req.Items {
			itemsSubtotal += it.UnitPriceARS * it.Quantity
		}

		total := itemsSubtotal + req.ShippingCostARS

		paidNow := total
		if req.PayPartial && req.PartialAmount > 0 && req.PartialAmount < total {
			paidNow = req.PartialAmount
		}

		remaining := total - paidNow
		isFullyPaid := remaining == 0
		deliveryStatus := "BLOQUEADO_POR_SALDO"
		if isFullyPaid {
			deliveryStatus = "LISTO_PARA_DESPACHAR"
		}

		orderID := time.Now().UnixNano() / 1000000
		orderNumber := fmt.Sprintf("KIDO-ORD-%d", orderID%1000000)

		initialPayment := PartialPayment{
			ID:            time.Now().UnixNano(),
			OrderID:       orderID,
			AmountARS:     paidNow,
			PaymentMethod: "Mercado Pago",
			MercadoPagoID: fmt.Sprintf("MP-PAY-%d", time.Now().UnixNano()%1000000),
			PaymentStatus: "approved",
			IsDownPayment: req.PayPartial,
			CreatedAt:     time.Now(),
		}

		newOrder := Order{
			ID:                  orderID,
			OrderNumber:         orderNumber,
			CustomerID:          req.CustomerID,
			CustomerEmail:       req.CustomerEmail,
			CustomerName:        req.CustomerName,
			TotalARS:            total,
			TotalPaidARS:        paidNow,
			RemainingBalanceARS: remaining,
			IsFullyPaid:         isFullyPaid,
			DeliveryStatus:      deliveryStatus,
			OrderType:           req.OrderType,
			ShippingMethod:      req.ShippingMethod,
			ShippingCostARS:     req.ShippingCostARS,
			ShippingPostalCode:  req.ShippingPostalCode,
			ShippingAddress:     req.ShippingAddress,
			Items:               req.Items,
			Payments:            []PartialPayment{initialPayment},
			CreatedAt:           time.Now(),
		}

		store.mu.Lock()
		store.orders = append([]Order{newOrder}, store.orders...)

		// Reducir stock
		for _, it := range req.Items {
			for j := range store.products {
				if store.products[j].ID == it.ProductID {
					if store.products[j].StockQuantity >= it.Quantity {
						store.products[j].StockQuantity -= it.Quantity
						if store.products[j].StockQuantity == 0 && store.products[j].Status != "PRE_VENTA" {
							store.products[j].Status = "AGOTADO"
						}
					}
					break
				}
			}
		}

		// Sumar compra a las estadísticas del usuario para el ranking
		for i := range store.users {
			if store.users[i].ID == req.CustomerID || strings.EqualFold(store.users[i].Email, req.CustomerEmail) {
				store.users[i].TotalPurchasesCount++
				break
			}
		}

		store.mu.Unlock()
		pgSaveOrder(newOrder)

		// Si Mercado Pago está configurado, generar preferencia Checkout Pro real
		var initPoint, sandboxInitPoint, prefID string
		var mpErr error
		if mpConfig.AccessToken != "" {
			scheme := "http"
			if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
				scheme = "https"
			}
			baseURL := scheme + "://" + r.Host
			prefID, initPoint, sandboxInitPoint, mpErr = createMPPreference(newOrder, paidNow, baseURL)
			if mpErr != nil {
				log.Printf("⚠️ [Mercado Pago] Error generando preferencia: %v", mpErr)
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":            true,
			"order":              newOrder,
			"payment_verified":   mpConfig.AccessToken == "",
			"delivery_status":    deliveryStatus,
			"init_point":         initPoint,
			"sandbox_init_point": sandboxInitPoint,
			"preference_id":      prefID,
			"is_real_mp":         mpConfig.AccessToken != "",
			"is_sandbox":         mpConfig.IsSandbox,
			"message":            "Orden registrada correctamente.",
		})
	})

	// Registrar Pago Parcial / Cancelación de Saldo en Pedido
	mux.HandleFunc("/api/orders/pay-partial", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			OrderID   int64 `json:"order_id"`
			AmountARS int   `json:"amount_ars"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.orders {
			if store.orders[i].ID == req.OrderID {
				ord := &store.orders[i]
				if ord.IsFullyPaid {
					http.Error(w, "El pedido ya está 100% abonado", http.StatusBadRequest)
					return
				}

				if req.AmountARS > ord.RemainingBalanceARS {
					req.AmountARS = ord.RemainingBalanceARS
				}

				ord.TotalPaidARS += req.AmountARS
				ord.RemainingBalanceARS -= req.AmountARS
				if ord.RemainingBalanceARS <= 0 {
					ord.RemainingBalanceARS = 0
					ord.IsFullyPaid = true
					ord.DeliveryStatus = "LISTO_PARA_DESPACHAR" // Se desbloquea entrega al completar 100%
				}

				// Si Mercado Pago real está activo, generar preferencia de cobro
				var initPoint, sandboxInitPoint, prefID string
				if mpConfig.AccessToken != "" {
					scheme := "http"
					if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
						scheme = "https"
					}
					baseURL := scheme + "://" + r.Host
					prefID, initPoint, sandboxInitPoint, _ = createMPPreference(*ord, req.AmountARS, baseURL)
				}

				payment := PartialPayment{
					ID:            time.Now().UnixNano(),
					OrderID:       ord.ID,
					AmountARS:     req.AmountARS,
					PaymentMethod: "Mercado Pago",
					MercadoPagoID: fmt.Sprintf("MP-PARTIAL-%d", time.Now().UnixNano()%1000000),
					PaymentStatus: "approved",
					IsDownPayment: false,
					CreatedAt:     time.Now(),
				}
				ord.Payments = append(ord.Payments, payment)
				pgSaveOrder(*ord)

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":               true,
					"order_id":              ord.ID,
					"amount_paid":           req.AmountARS,
					"remaining_balance_ars": ord.RemainingBalanceARS,
					"is_fully_paid":         ord.IsFullyPaid,
					"delivery_status":       ord.DeliveryStatus,
					"init_point":            initPoint,
					"sandbox_init_point":    sandboxInitPoint,
					"preference_id":         prefID,
					"is_real_mp":            mpConfig.AccessToken != "",
					"message":               "Pago de saldo procesado exitosamente.",
				})
				return
			}
		}

		http.Error(w, "Pedido no encontrado", http.StatusNotFound)
	})

	// Actualizar Estado de Entrega de Pedido (Admin / Logística)
	mux.HandleFunc("/api/orders/update-delivery-status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			OrderID int64  `json:"order_id"`
			Status  string `json:"status"` // "BLOQUEADO_POR_SALDO", "LISTO_PARA_DESPACHAR", "EN_CAMINO", "ENTREGADO"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.orders {
			if store.orders[i].ID == req.OrderID {
				store.orders[i].DeliveryStatus = req.Status
				pgSaveOrder(store.orders[i])

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":         true,
					"order_id":        req.OrderID,
					"delivery_status": req.Status,
				})
				return
			}
		}
		http.Error(w, "Pedido no encontrado", http.StatusNotFound)
	})

	// Actualizar Tracking Number y Empresa de Envíos (Admin)
	mux.HandleFunc("/api/admin/orders/update-tracking", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			OrderID         int64  `json:"order_id"`
			TrackingNumber  string `json:"tracking_number"`
			TrackingCarrier string `json:"tracking_carrier"` // "Correo Argentino", "Andreani", "Otro"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.orders {
			if store.orders[i].ID == req.OrderID {
				store.orders[i].TrackingNumber = strings.TrimSpace(req.TrackingNumber)
				store.orders[i].TrackingCarrier = strings.TrimSpace(req.TrackingCarrier)
				// Si se asigna tracking y no está finalizado/entregado, marcar como EN_CAMINO
				if store.orders[i].TrackingNumber != "" && store.orders[i].DeliveryStatus != "ENTREGADO" {
					store.orders[i].DeliveryStatus = "EN_CAMINO"
				}
				pgSaveOrder(store.orders[i])

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":          true,
					"order_id":         req.OrderID,
					"tracking_number":  store.orders[i].TrackingNumber,
					"tracking_carrier": store.orders[i].TrackingCarrier,
					"delivery_status":  store.orders[i].DeliveryStatus,
					"order":            store.orders[i],
					"message":          "Código de seguimiento y transporte actualizados correctamente.",
				})
				return
			}
		}
		http.Error(w, "Pedido no encontrado", http.StatusNotFound)
	})

	// Calculador de Envíos y Tarifas por Código Postal
	mux.HandleFunc("/api/shipping/calculate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			PostalCode string `json:"postal_code"`
			CartTotal  int    `json:"cart_total"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		zone, options := calculateShippingRates(req.PostalCode, req.CartTotal)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":                 true,
			"postal_code":             strings.TrimSpace(req.PostalCode),
			"zone_name":               zone,
			"free_shipping_threshold": 80000,
			"has_free_shipping":       req.CartTotal >= 80000,
			"options":                 options,
		})
	})

	// Portal de Autoservicio del Cliente ("Mi Cuenta / Mis Pedidos")
	mux.HandleFunc("/api/user/portal-data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		email := strings.TrimSpace(r.URL.Query().Get("email"))
		if email == "" {
			http.Error(w, "Email requerido", http.StatusBadRequest)
			return
		}

		store.mu.RLock()
		defer store.mu.RUnlock()

		// 1. Encontrar usuario
		var matchedUser *User
		for i := range store.users {
			if strings.EqualFold(store.users[i].Email, email) {
				matchedUser = &store.users[i]
				break
			}
		}

		// 2. Pedidos del usuario
		var userOrders []Order
		var totalPendingARS int
		for _, o := range store.orders {
			if strings.EqualFold(o.CustomerEmail, email) {
				userOrders = append(userOrders, o)
				totalPendingARS += o.RemainingBalanceARS
			}
		}

		// 3. Rifas del usuario
		type UserRaffleTicketInfo struct {
			Number       int    `json:"number"`
			IsFreeTicket bool   `json:"is_free_ticket"`
			PurchasedAt  string `json:"purchased_at"`
			IsWinner     bool   `json:"is_winner"`
		}

		type UserRaffleSummary struct {
			RaffleID         int64                  `json:"raffle_id"`
			RaffleNumber     string                 `json:"raffle_number"`
			Title            string                 `json:"title"`
			PrizeDescription string                 `json:"prize_description"`
			PrizeImage       string                 `json:"prize_image"`
			DrawDatetime     string                 `json:"draw_datetime"`
			Status           string                 `json:"status"`
			WinnerNumber     *int                   `json:"winner_number,omitempty"`
			UserHasWinner    bool                   `json:"user_has_winner"`
			Tickets          []UserRaffleTicketInfo `json:"tickets"`
		}

		var userRaffles []UserRaffleSummary
		for _, raf := range store.raffles {
			var myTickets []UserRaffleTicketInfo
			hasWinner := false

			for num, ticket := range raf.Tickets {
				if strings.EqualFold(ticket.CustomerEmail, email) {
					isWin := raf.WinnerNumber != nil && *raf.WinnerNumber == num
					if isWin {
						hasWinner = true
					}
					myTickets = append(myTickets, UserRaffleTicketInfo{
						Number:       num,
						IsFreeTicket: ticket.IsFreeTicket,
						PurchasedAt:  ticket.PurchasedAt.Format("02/01/2006 15:04"),
						IsWinner:     isWin,
					})
				}
			}

			if len(myTickets) > 0 {
				prizeImg := "https://via.placeholder.com/300"
				if len(raf.PrizeImages) > 0 && raf.PrizeImages[0] != "" {
					prizeImg = raf.PrizeImages[0]
				}

				userRaffles = append(userRaffles, UserRaffleSummary{
					RaffleID:         raf.ID,
					RaffleNumber:     raf.RaffleNumber,
					Title:            raf.Title,
					PrizeDescription: raf.PrizeDescription,
					PrizeImage:       prizeImg,
					DrawDatetime:     raf.DrawDatetime,
					Status:           raf.Status,
					WinnerNumber:     raf.WinnerNumber,
					UserHasWinner:    hasWinner,
					Tickets:          myTickets,
				})
			}
		}

		// 4. Progreso de Cliente Frecuente
		months := 1
		isFreq := false
		pts := 0
		purchases := len(userOrders)
		freeAvailable := false

		if matchedUser != nil {
			months = matchedUser.ConsecutiveMonths
			isFreq = matchedUser.IsFrequentCustomer
			pts = matchedUser.FrequentPoints
			if matchedUser.TotalPurchasesCount > purchases {
				purchases = matchedUser.TotalPurchasesCount
			}
			freeAvailable = isFreq && !matchedUser.HasClaimedFreeRaffle
		}

		targetMonths := 3
		progressPct := 100
		if !isFreq {
			progressPct = int((float64(months) / float64(targetMonths)) * 100)
			if progressPct > 100 {
				progressPct = 100
			}
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"user":    matchedUser,
			"orders":  userOrders,
			"raffles": userRaffles,
			"stats": map[string]interface{}{
				"total_orders":          len(userOrders),
				"total_pending_ars":     totalPendingARS,
				"active_raffles_count":  len(userRaffles),
				"consecutive_months":    months,
				"target_months":         targetMonths,
				"progress_percentage":   progressPct,
				"is_frequent_customer":  isFreq,
				"frequent_points":       pts,
				"total_purchases_count": purchases,
				"free_raffle_available": freeAvailable,
			},
		})
	})

	// ==========================================
	// USUARIOS (ADMIN VIEW)
	// ==========================================
	mux.HandleFunc("/api/admin/users", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		store.mu.RLock()
		defer store.mu.RUnlock()

		var safeUsers []map[string]interface{}
		for _, u := range store.users {
			safeUsers = append(safeUsers, map[string]interface{}{
				"id":                    u.ID,
				"email":                 u.Email,
				"role":                  u.Role,
				"name":                  u.FirstName + " " + u.LastName,
				"phone":                 u.Phone,
				"address":               fmt.Sprintf("%s %s, %s", u.Street, u.StreetNumber, u.Locality),
				"consecutive_months":    u.ConsecutiveMonths,
				"is_frequent_customer":  u.IsFrequentCustomer,
				"frequent_points":       u.FrequentPoints,
				"total_purchases_count": u.TotalPurchasesCount,
				"avatar_url":            u.AvatarURL,
			})
		}

		sort.Slice(safeUsers, func(i, j int) bool {
			return safeUsers[i]["id"].(int64) > safeUsers[j]["id"].(int64)
		})

		json.NewEncoder(w).Encode(safeUsers)
	}))

	// Toggle / Otorgar insignia de Cliente Frecuente
	mux.HandleFunc("/api/admin/users/toggle-frequent", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			UserID     int64 `json:"user_id"`
			ID         int64 `json:"id"`
			IsFrequent bool  `json:"is_frequent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		userID := req.UserID
		if userID == 0 {
			userID = req.ID
		}
		if userID == 0 {
			http.Error(w, "ID de usuario inválido", http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.users {
			if store.users[i].ID == userID {
				store.users[i].IsFrequentCustomer = req.IsFrequent
				if req.IsFrequent {
					// Otorgar insignia: asegurar al menos 4 meses consecutivos (>3) y 1 punto
					if store.users[i].ConsecutiveMonths < 4 {
						store.users[i].ConsecutiveMonths = 4
					}
					if store.users[i].FrequentPoints < 1 {
						store.users[i].FrequentPoints = 1
					}
				} else {
					// Quitar insignia: resetear meses consecutivos a 0 y puntos
					store.users[i].ConsecutiveMonths = 0
					store.users[i].FrequentPoints = 0
				}

				pgSaveUser(store.users[i])

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":              true,
					"user_id":              store.users[i].ID,
					"is_frequent_customer": store.users[i].IsFrequentCustomer,
					"consecutive_months":   store.users[i].ConsecutiveMonths,
					"frequent_points":      store.users[i].FrequentPoints,
					"message":              "Insignia de cliente frecuente actualizada correctamente.",
				})
				return
			}
		}

		http.Error(w, "Usuario no encontrado", http.StatusNotFound)
	}))

	// ==========================================
	// MÉTRICAS & CONTABILIDAD (ADMIN DASHBOARD)
	// ==========================================
	mux.HandleFunc("/api/admin/metrics", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		store.mu.RLock()
		defer store.mu.RUnlock()

		totalStockUnits := 0
		totalInventoryValARS := 0.0
		preOrderUnits := 0
		for _, p := range store.products {
			totalStockUnits += p.StockQuantity
			totalInventoryValARS += float64(p.PriceARS * p.StockQuantity)
			if p.Status == "PRE_VENTA" {
				preOrderUnits += p.StockQuantity
			}
		}

		totalExpensesARS := 0.0
		for _, e := range store.expenses {
			totalExpensesARS += e.AmountARS
		}

		totalSalesARS := 0
		totalCollectedARS := 0
		totalPendingARS := 0
		for _, o := range store.orders {
			totalSalesARS += o.TotalARS
			totalCollectedARS += o.TotalPaidARS
			totalPendingARS += o.RemainingBalanceARS
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"total_products":            len(store.products),
			"total_stock_units":         totalStockUnits,
			"total_inventory_val_ars":   totalInventoryValARS,
			"pre_orders_units":          preOrderUnits,
			"month_expenses_ars":        totalExpensesARS,
			"total_sales_ars":           totalSalesARS,
			"total_collected_ars":       totalCollectedARS,
			"total_pending_balance_ars": totalPendingARS,
			"total_orders_count":        len(store.orders),
			"active_raffles_count":      len(store.raffles),
			"db_status":                 "PostgreSQL kido_db (localhost:5432)",
			"db_active":                 dbActive,
		})
	}))

	// Gastos
	mux.HandleFunc("/api/admin/expenses", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var newExp Expense
			if err := json.NewDecoder(r.Body).Decode(&newExp); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			store.mu.Lock()
			newExp.ID = len(store.expenses) + 1
			newExp.CreatedAt = time.Now()
			if newExp.Date == "" {
				newExp.Date = time.Now().Format("2006-01-02")
			}
			store.expenses = append([]Expense{newExp}, store.expenses...)
			store.mu.Unlock()
			pgSaveExpense(newExp)
			json.NewEncoder(w).Encode(newExp)
			return
		}

		store.mu.RLock()
		defer store.mu.RUnlock()
		expensesCopy := make([]Expense, len(store.expenses))
		copy(expensesCopy, store.expenses)
		sort.Slice(expensesCopy, func(i, j int) bool {
			return expensesCopy[i].ID > expensesCopy[j].ID
		})
		json.NewEncoder(w).Encode(expensesCopy)
	}))

	// Webhook de Mercado Pago
	mux.HandleFunc("/api/webhooks/mercadopago", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok","message":"Mercado Pago webhook endpoint ready"}`))
			return
		}

		var payload struct {
			Action string `json:"action"`
			Type   string `json:"type"`
			Data   struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		paymentID := payload.Data.ID
		if paymentID == "" {
			paymentID = r.URL.Query().Get("data.id")
		}
		if paymentID == "" {
			paymentID = r.URL.Query().Get("id")
		}

		log.Printf("🔔 [Webhook MP] Notificación recibida: Action=%s Type=%s PaymentID=%s", payload.Action, payload.Type, paymentID)

		if paymentID != "" && mpConfig.AccessToken != "" {
			go processMPPayment(paymentID)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"received":true}`))
	})

	// Configuración de Mercado Pago (Admin Panel)
	mux.HandleFunc("/api/admin/mercadopago/config", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var newCfg MPConfig
			if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			newCfg.AccessToken = strings.TrimSpace(newCfg.AccessToken)
			newCfg.PublicKey = strings.TrimSpace(newCfg.PublicKey)

			var accountInfo map[string]interface{}
			if newCfg.AccessToken != "" {
				testReq, _ := http.NewRequest("GET", "https://api.mercadopago.com/users/me", nil)
				testReq.Header.Set("Authorization", "Bearer "+newCfg.AccessToken)
				client := &http.Client{Timeout: 6 * time.Second}
				resp, err := client.Do(testReq)
				if err != nil || resp.StatusCode != http.StatusOK {
					http.Error(w, "El Access Token ingresado no es válido o ha expirado en Mercado Pago Developers", http.StatusBadRequest)
					return
				}
				_ = json.NewDecoder(resp.Body).Decode(&accountInfo)
				resp.Body.Close()
			}

			if err := saveMPConfig(newCfg); err != nil {
				http.Error(w, "Error guardando configuración", http.StatusInternalServerError)
				return
			}

			json.NewEncoder(w).Encode(map[string]interface{}{
				"success":       true,
				"message":       "Credenciales de Mercado Pago guardadas y validadas exitosamente",
				"is_sandbox":    mpConfig.IsSandbox,
				"is_configured": mpConfig.AccessToken != "",
				"masked_token":  maskToken(mpConfig.AccessToken),
				"account_info":  accountInfo,
			})
			return
		}

		// GET
		json.NewEncoder(w).Encode(map[string]interface{}{
			"is_configured": mpConfig.AccessToken != "",
			"is_sandbox":    mpConfig.IsSandbox,
			"masked_token":  maskToken(mpConfig.AccessToken),
			"public_key":    mpConfig.PublicKey,
			"webhook_url":   "/api/webhooks/mercadopago",
		})
	}))

	// Purchase Orders
	mux.HandleFunc("/api/admin/purchase-orders", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var newPO PurchaseOrder
			if err := json.NewDecoder(r.Body).Decode(&newPO); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			store.mu.Lock()
			newPO.ID = len(store.purchaseOrders) + 1
			newPO.CreatedAt = time.Now()
			newPO.PONumber = fmt.Sprintf("PO-2026-%04d", newPO.ID+90)
			newPO.OrderDate = time.Now().Format("2006-01-02")
			if newPO.Status == "" {
				newPO.Status = "EMITIDA"
			}
			store.purchaseOrders = append([]PurchaseOrder{newPO}, store.purchaseOrders...)
			store.mu.Unlock()
			json.NewEncoder(w).Encode(newPO)
			return
		}

		store.mu.RLock()
		defer store.mu.RUnlock()
		posCopy := make([]PurchaseOrder, len(store.purchaseOrders))
		copy(posCopy, store.purchaseOrders)
		sort.Slice(posCopy, func(i, j int) bool {
			return posCopy[i].ID > posCopy[j].ID
		})
		json.NewEncoder(w).Encode(posCopy)
	}))

	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		if pNum, err := strconv.Atoi(p); err == nil && pNum > 0 {
			port = pNum
		}
	}

	// Wrapper global con CORS y Rate Limiter
	handler := corsMiddleware(rateLimitMiddleware(mux))

	fmt.Printf("\n======================================================\n")
	fmt.Printf("🚀 SERVIDOR KIDO DIECAST & COLLECTIBLES (ECOMMERCE & ADMIN)\n")
	fmt.Printf("👉 Tienda Frontend:     http://localhost:%d/\n", port)
	fmt.Printf("👉 Panel Administración: http://localhost:%d/admin.html\n", port)
	fmt.Printf("👉 API Productos:       http://localhost:%d/api/products\n", port)
	fmt.Printf("👉 API Rankings:        http://localhost:%d/api/rankings\n", port)
	fmt.Printf("👉 API Rifas & Sorteos: http://localhost:%d/api/raffles\n", port)
	fmt.Printf("🛡️  Seguridad: Rate Limiting & Admin Auth ACTIVOS\n")
	fmt.Printf("======================================================\n\n")

	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), handler); err != nil {
		log.Fatalf("Error iniciando servidor: %v", err)
	}
}
