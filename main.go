package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ==========================================
// MODELOS DE DATOS
// ==========================================

type User struct {
	ID                     int64  `json:"id"`
	Email                  string `json:"email"` // Nombre de usuario
	Password               string `json:"password,omitempty"`
	Role                   string `json:"role"` // "ADMIN" o "CLIENT"
	GoogleID               string `json:"google_id,omitempty"`
	FirstName              string `json:"first_name"`
	LastName               string `json:"last_name"`
	Phone                  string `json:"phone"`
	Locality               string `json:"locality"`
	Street                 string `json:"street"`
	StreetNumber           string `json:"street_number"`
	AvatarURL              string `json:"avatar_url"`
	ConsecutiveMonths      int    `json:"consecutive_months"` // Meses seguidos con compra
	IsFrequentCustomer     bool   `json:"is_frequent_customer"` // true si > 3 meses
	FrequentPoints         int    `json:"frequent_points"`     // 1 pt por cada mes extra después de los 3 meses
	TotalPurchasesCount    int    `json:"total_purchases_count"` // 1 pt por cada compra
	HasClaimedFreeRaffle   bool   `json:"has_claimed_free_raffle"` // 1 ticket gratis por sorteo
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

type Product struct {
	ID             int64            `json:"id"`
	Title          string           `json:"title"`
	Handle         string           `json:"handle"`
	BodyHTML       string           `json:"body_html"`
	Vendor         string           `json:"vendor"`
	ProductType    string           `json:"product_type"` // "Autito", "Remera", "Sticker"
	Scale          string           `json:"scale,omitempty"` // Requerido si Autito (1:64, 1:43, 1:18)
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
}

type ProductCatalog struct {
	Products []Product `json:"products"`
}

type PartialPayment struct {
	ID              int64     `json:"id"`
	OrderID         int64     `json:"order_id"`
	ProductID       int64     `json:"product_id"`
	AmountARS       int       `json:"amount_ars"`
	PaymentMethod   string    `json:"payment_method"`
	MercadoPagoID   string    `json:"mercadopago_id"`
	PaymentStatus   string    `json:"payment_status"` // "approved"
	IsDownPayment   bool      `json:"is_downpayment"` // Seña/reserva
	CreatedAt       time.Time `json:"created_at"`
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
	DeliveryStatus      string           `json:"delivery_status"` // "BLOQUEADO_POR_SALDO" o "LISTO_PARA_DESPACHAR"
	OrderType           string           `json:"order_type"` // "VENTA_DIRECTA" o "PRE_VENTA_CON_SEÑA"
	Items               []OrderItem      `json:"items"`
	Payments            []PartialPayment `json:"payments"`
	CreatedAt           time.Time        `json:"created_at"`
}

type RaffleTicket struct {
	Number           int       `json:"number"`
	CustomerID       int64     `json:"customer_id"`
	CustomerEmail    string    `json:"customer_email"`
	CustomerName     string    `json:"customer_name"`
	IsFreeTicket     bool      `json:"is_free_ticket"`
	MercadoPagoID    string    `json:"mercadopago_id"`
	PurchasedAt      time.Time `json:"purchased_at"`
}

type Raffle struct {
	ID               int64               `json:"id"`
	RaffleNumber     string              `json:"raffle_number"` // "RIFA-#01-CHASE-R34"
	Title            string              `json:"title"`
	PrizeDescription string              `json:"prize_description"`
	PrizeImages      []string            `json:"prize_images"`
	StartDatetime    string              `json:"start_datetime"`
	EndDatetime      string              `json:"end_datetime"`
	DrawDatetime     string              `json:"draw_datetime"`
	MinNumber        int                 `json:"min_number"` // ej: 0
	MaxNumber        int                 `json:"max_number"` // ej: 99
	TicketPriceARS   int                 `json:"ticket_price_ars"`
	Status           string              `json:"status"` // "ACTIVA", "FINALIZADA", "SORTEADA"
	Tickets          map[int]RaffleTicket `json:"tickets"` // number -> ticket
	WinnerNumber     *int                `json:"winner_number,omitempty"`
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
}

var store = &StoreData{
	users: []User{
		{
			ID:                  1,
			Email:               "admin@kido.com.ar",
			Password:            "admin123",
			Role:                "ADMIN",
			FirstName:           "Kido",
			LastName:            "Admin",
			Phone:               "+54 11 5555-0100",
			Locality:            "CABA",
			Street:              "Av. Cabildo",
			StreetNumber:        "2400",
			AvatarURL:           "https://images.unsplash.com/photo-1534528741775-53994a69daeb?auto=format&fit=crop&w=200&q=80",
			ConsecutiveMonths:   12,
			IsFrequentCustomer:  true,
			FrequentPoints:      9,
			TotalPurchasesCount: 25,
		},
		{
			ID:                  2,
			Email:               "martin@kido.com.ar",
			Password:            "cliente123",
			Role:                "CLIENT",
			FirstName:           "Martín",
			LastName:            "Gómez",
			Phone:               "+54 11 4444-1234",
			Locality:            "Vicente López",
			Street:              "Av. del Libertador",
			StreetNumber:        "1540",
			AvatarURL:           "https://images.unsplash.com/photo-1535713875002-d1d0cf377fde?auto=format&fit=crop&w=200&q=80",
			ConsecutiveMonths:   4,    // > 3 meses: CLIENTE FRECUENTE ACTIVO
			IsFrequentCustomer:  true, // ¡Cliente Frecuente!
			FrequentPoints:      1,    // 1 mes extra después de los 3 meses
			TotalPurchasesCount: 8,    // 8 puntos por compras
			HasClaimedFreeRaffle: false,
		},
		{
			ID:                  3,
			Email:               "lucas@kido.com.ar",
			Password:            "cliente123",
			Role:                "CLIENT",
			FirstName:           "Lucas",
			LastName:            "Pérez",
			Phone:               "+54 11 3333-7890",
			Locality:            "San Isidro",
			Street:              "Centenario",
			StreetNumber:        "820",
			AvatarURL:           "https://images.unsplash.com/photo-1570295999919-56ceb5ecca61?auto=format&fit=crop&w=200&q=80",
			ConsecutiveMonths:   2,     // 2 meses: LE FALTA 1 MES PARA FRECUENTE
			IsFrequentCustomer:  false, // Aún no es frecuente
			FrequentPoints:      0,
			TotalPurchasesCount: 3,
			HasClaimedFreeRaffle: false,
		},
	},
	expenses: []Expense{
		{ID: 1, Category: "Flete Internacional", Concept: "Envío aéreo lote Mini GT & Pop Race (Hong Kong -> EZE)", Supplier: "DHL Express Cargo", AmountARS: 540000, AmountUSD: 400, InvoiceNumber: "DHL-98442", Date: "2026-09-15"},
		{ID: 2, Category: "Packaging Coleccionista", Concept: "Cajas de cartón corrugado triple onda + Pluribol burbujas antishock", Supplier: "Embalajes Colección SRL", AmountARS: 125000, AmountUSD: 92.5, InvoiceNumber: "FAC-B-000412", Date: "2026-09-18"},
		{ID: 3, Category: "Aduana / Impuestos", Concept: "Arancel nacionalización despacho importación Diecast", Supplier: "Aduana Argentina / Courier", AmountARS: 890000, AmountUSD: 660, InvoiceNumber: "DJAI-2026-901", Date: "2026-09-20"},
	},
	purchaseOrders: []PurchaseOrder{
		{ID: 1, PONumber: "PO-2026-0089", SupplierName: "M&J Toys Inc. / Mini GT Distribution", Status: "EN_TRANSITO", OrderDate: "2026-09-10", ExpectedETA: "Nov 2026", TotalUSD: 3450.00, TrackingNum: "AWB-77492019"},
		{ID: 2, PONumber: "PO-2026-0090", SupplierName: "Pop Race HK Models", Status: "EMITIDA", OrderDate: "2026-09-22", ExpectedETA: "Dic 2026", TotalUSD: 1890.00, TrackingNum: "PENDIENTE"},
	},
}

// Inicializar Rifa de Ejemplo
func initRaffles() {
	sampleRaffle := Raffle{
		ID:               1,
		RaffleNumber:     "RIFA-#01-CHASE-R34",
		Title:            "Mini GT 1:64 Nissan Skyline GT-R C-West Brian O'Conner CHASE EDITION",
		PrizeDescription: "Versión Chase ultra rara 1 de 24 sellada de fábrica. Acabado pulido con llantas especiales y tarjeta numerada.",
		PrizeImages: []string{
			"https://cdn.shopify.com/s/files/1/0978/6929/9988/files/797135489_1617113229805704_600506348130599165_n.jpg?v=1790150116",
		},
		StartDatetime:  "2026-09-20 10:00",
		EndDatetime:    "2026-10-15 20:00",
		DrawDatetime:   "2026-10-16 21:00 (Lotería Nacional Nocturna)",
		MinNumber:      0,
		MaxNumber:      99,
		TicketPriceARS: 2500,
		Status:         "ACTIVA",
		Tickets:        make(map[int]RaffleTicket),
	}

	// Sembrar algunos números vendidos
	soldNumbers := []int{1, 2, 8, 12, 18, 23, 33, 45, 50, 68, 77, 88}
	for _, n := range soldNumbers {
		sampleRaffle.Tickets[n] = RaffleTicket{
			Number:        n,
			CustomerID:    99,
			CustomerEmail: fmt.Sprintf("coleccionista%d@gmail.com", n),
			CustomerName:  fmt.Sprintf("Coleccionista #%d", n),
			MercadoPagoID: fmt.Sprintf("MP-TICKET-%d", n),
			PurchasedAt:   time.Now().Add(-time.Duration(n) * time.Hour),
		}
	}

	store.raffles = []Raffle{sampleRaffle}
}

// Inicializar Pedidos de Ejemplo con Pagos Parciales
func initOrders() {
	store.orders = []Order{
		{
			ID:                  1001,
			OrderNumber:         "KIDO-ORD-1001",
			CustomerID:          2, // Martín (Cliente Frecuente)
			CustomerEmail:       "martin@kido.com.ar",
			CustomerName:        "Martín Gómez",
			TotalARS:            45980,
			TotalPaidARS:        20000,
			RemainingBalanceARS: 25980,
			IsFullyPaid:         false,
			DeliveryStatus:      "BLOQUEADO_POR_SALDO", // No se entrega hasta completar pago
			OrderType:           "PRE_VENTA_CON_SEÑA",
			Items: []OrderItem{
				{ProductID: 10390924034324, ProductTitle: "Mini GT 1:64 Nissan Skyline GT-R C-West 2 Fast 2 Furious", Quantity: 2, UnitPriceARS: 22990, SubtotalARS: 45980, ProductType: "Autito", ScaleOrSize: "1:64"},
			},
			Payments: []PartialPayment{
				{ID: 1, OrderID: 1001, ProductID: 10390924034324, AmountARS: 20000, PaymentMethod: "Mercado Pago", MercadoPagoID: "MP-SEÑA-9921", PaymentStatus: "approved", IsDownPayment: true, CreatedAt: time.Now().Add(-5 * 24 * time.Hour)},
			},
			CreatedAt: time.Now().Add(-5 * 24 * time.Hour),
		},
		{
			ID:                  1002,
			OrderNumber:         "KIDO-ORD-1002",
			CustomerID:          3, // Lucas
			CustomerEmail:       "lucas@kido.com.ar",
			CustomerName:        "Lucas Pérez",
			TotalARS:            18500,
			TotalPaidARS:        18500,
			RemainingBalanceARS: 0,
			IsFullyPaid:         true,
			DeliveryStatus:      "LISTO_PARA_DESPACHAR", // Pago 100% completado
			OrderType:           "VENTA_DIRECTA",
			Items: []OrderItem{
				{ProductID: 999001, ProductTitle: "Pop Race Mazda RX-7 RE-Amemiya Chrome", Quantity: 1, UnitPriceARS: 18500, SubtotalARS: 18500, ProductType: "Autito", ScaleOrSize: "1:64"},
			},
			Payments: []PartialPayment{
				{ID: 2, OrderID: 1002, ProductID: 999001, AmountARS: 18500, PaymentMethod: "Mercado Pago", MercadoPagoID: "MP-FULL-8812", PaymentStatus: "approved", IsDownPayment: false, CreatedAt: time.Now().Add(-2 * 24 * time.Hour)},
			},
			CreatedAt: time.Now().Add(-2 * 24 * time.Hour),
		},
	}
}

// Cargar Catálogo Base
func loadCatalog() {
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
			ID:             999001,
			Title:          "Remera Oversize JDM Culture Black - KIDO Garage",
			Handle:         "remera-oversize-jdm-culture-black",
			BodyHTML:       "<p>Remera 100% Algodón Peinado 24/1 pesado. Estampa serigráfica de alta durabilidad en espalda y pecho. Corte boxy fit oversize japonés.</p>",
			Vendor:         "KIDO Apparel",
			ProductType:    "Remera",
			ApparelSize:    "L (Oversize)",
			Scale:          "",
			Tags:           []string{"Indumentaria", "Remeras", "JDM", "Streetwear"},
			PriceARS:       14000,
			PriceUSD:       "10.50",
			Status:         "STOCK",
			StockQuantity:  40,
			IsActive:       true,
			GalleryImages:  []string{"https://images.unsplash.com/photo-1521572267360-ee0c2909d518?auto=format&fit=crop&w=800&q=80"},
			ShortVideoURL:  "",
		},
		{
			ID:             999002,
			Title:          "Pack x10 Stickers Vinilo Holográfico Resistente al Agua",
			Handle:         "pack-10-stickers-vinilo-diecast-jdm",
			BodyHTML:       "<p>Pack de 10 stickers troquelados de vinilo premium con laminado UV resistente a la intemperie y agua. Diseños exclusivos de Kaido House, Mini GT, Pop Race y KIDO Garage.</p>",
			Vendor:         "KIDO Accessories",
			ProductType:    "Sticker",
			Scale:          "",
			ApparelSize:    "",
			Tags:           []string{"Accesorios", "Stickers", "Vinyl", "JDM"},
			PriceARS:       3500,
			PriceUSD:       "2.60",
			Status:         "STOCK",
			StockQuantity:  100,
			IsActive:       true,
			GalleryImages:  []string{"https://images.unsplash.com/photo-1589384267710-7a170981ca78?auto=format&fit=crop&w=800&q=80"},
			ShortVideoURL:  "",
		},
		{
			ID:             999003,
			Title:          "Buzo Hoodie JDM Kanjozoku Night Runner",
			Handle:         "buzo-hoodie-jdm-kanjozoku-night-runner",
			BodyHTML:       "<p>Buzo canguro con frisa invisible premium, interior abrigado y capucha forrada. Gráficos inspirados en el Loop One de Osaka.</p>",
			Vendor:         "KIDO Apparel",
			ProductType:    "Remera",
			ApparelSize:    "XL",
			Scale:          "",
			Tags:           []string{"Indumentaria", "Hoodies", "JDM"},
			PriceARS:       38000,
			PriceUSD:       "28.00",
			Status:         "STOCK",
			StockQuantity:  15,
			IsActive:       true,
			GalleryImages:  []string{"https://images.unsplash.com/photo-1556905055-8f358a7a47b2?auto=format&fit=crop&w=800&q=80"},
			ShortVideoURL:  "",
		},
	}

	catalog.Products = append(apparelAndStickers, catalog.Products...)

	store.mu.Lock()
	store.products = catalog.Products
	store.mu.Unlock()

	log.Printf("Catálogo cargado: %d productos listos para KIDO", len(store.products))
}

// ==========================================
// SERVIDOR HTTP & APIS
// ==========================================

func main() {
	loadCatalog()
	initRaffles()
	initOrders()

	mux := http.NewServeMux()

	// 1. Archivos estáticos en /public
	fs := http.FileServer(http.Dir("./public"))
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

		newUser := User{
			ID:                  int64(len(store.users) + 1),
			Email:               req.Email,
			Password:            req.Password,
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

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"user":    newUser,
			"message": "¡Cuenta de cliente creada exitosamente!",
		})
	})

	// Login
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
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

		store.mu.RLock()
		defer store.mu.RUnlock()

		for _, u := range store.users {
			if strings.EqualFold(u.Email, req.Email) && u.Password == req.Password {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"user":    u,
				})
				return
			}
		}

		http.Error(w, "Credenciales incorrectas", http.StatusUnauthorized)
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
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"user":    u,
					"message": "Sesión iniciada con Google",
				})
				return
			}
		}

		// Crear nuevo usuario desde Google
		newUser := User{
			ID:                  int64(len(store.users) + 1),
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

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"user":    newUser,
			"message": "Registro completado con cuenta de Google",
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
				json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"user":    store.users[i],
				})
				return
			}
		}

		http.Error(w, "Usuario no encontrado", http.StatusNotFound)
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

		json.NewEncoder(w).Encode(map[string]interface{}{
			"count":    len(filtered),
			"products": filtered,
		})
	})

	// Crear Nuevo Artículo (Admin)
	mux.HandleFunc("/api/admin/products", func(w http.ResponseWriter, r *http.Request) {
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

		p.ID = time.Now().UnixNano()
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

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"product": p,
			"message": "Artículo agregado correctamente al catálogo",
		})
	})

	// Carga Masiva de Stock
	mux.HandleFunc("/api/admin/stock/bulk-add", func(w http.ResponseWriter, r *http.Request) {
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
	})

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

		json.NewEncoder(w).Encode(map[string]interface{}{
			"raffles": response,
		})
	})

	// Crear Nueva Rifa (Admin)
	mux.HandleFunc("/api/admin/raffles", func(w http.ResponseWriter, r *http.Request) {
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

		newRaffle := Raffle{
			ID:               int64(len(store.raffles) + 1),
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

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"raffle":  newRaffle,
			"message": "Rifa creada y abierta al público",
		})
	})

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

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":          true,
			"ticket_number":    req.Number,
			"is_free":          isFree,
			"mercadopago_id":   mpID,
			"message":          fmt.Sprintf("¡Número %d asignado con éxito para %s!", req.Number, req.CustomerName),
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
				json.NewEncoder(w).Encode(userOrders)
				return
			}
			json.NewEncoder(w).Encode(store.orders)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			CustomerID      int64       `json:"customer_id"`
			CustomerEmail   string      `json:"customer_email"`
			CustomerName    string      `json:"customer_name"`
			Items           []OrderItem `json:"items"`
			PayPartial      bool        `json:"pay_partial"`       // true si paga seña / reserva
			PartialAmount   int         `json:"partial_amount"`    // monto pagado ahora
			OrderType       string      `json:"order_type"`        // "VENTA_DIRECTA" o "PRE_VENTA_CON_SEÑA"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		total := 0
		for _, it := range req.Items {
			total += it.UnitPriceARS * it.Quantity
		}

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

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":          true,
			"order":            newOrder,
			"payment_verified": true,
			"delivery_status":  deliveryStatus,
			"message":          "Pago registrado a través de Mercado Pago.",
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

				json.NewEncoder(w).Encode(map[string]interface{}{
					"success":                true,
					"order_id":               ord.ID,
					"amount_paid":            req.AmountARS,
					"remaining_balance_ars":  ord.RemainingBalanceARS,
					"is_fully_paid":          ord.IsFullyPaid,
					"delivery_status":        ord.DeliveryStatus,
					"message":                "Pago parcial registrado con Mercado Pago.",
				})
				return
			}
		}

		http.Error(w, "Pedido no encontrado", http.StatusNotFound)
	})

	// ==========================================
	// USUARIOS (ADMIN VIEW)
	// ==========================================
	mux.HandleFunc("/api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		store.mu.RLock()
		defer store.mu.RUnlock()

		var safeUsers []map[string]interface{}
		for _, u := range store.users {
			safeUsers = append(safeUsers, map[string]interface{}{
				"id":                     u.ID,
				"email":                  u.Email,
				"role":                   u.Role,
				"name":                   u.FirstName + " " + u.LastName,
				"phone":                  u.Phone,
				"address":                fmt.Sprintf("%s %s, %s", u.Street, u.StreetNumber, u.Locality),
				"consecutive_months":     u.ConsecutiveMonths,
				"is_frequent_customer":   u.IsFrequentCustomer,
				"frequent_points":        u.FrequentPoints,
				"total_purchases_count":  u.TotalPurchasesCount,
				"avatar_url":             u.AvatarURL,
			})
		}

		json.NewEncoder(w).Encode(safeUsers)
	})

	// Toggle / Otorgar insignia de Cliente Frecuente
	mux.HandleFunc("/api/admin/users/toggle-frequent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			UserID     int64 `json:"user_id"`
			IsFrequent bool  `json:"is_frequent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		store.mu.Lock()
		defer store.mu.Unlock()

		for i := range store.users {
			if store.users[i].ID == req.UserID {
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
	})

	// ==========================================
	// MÉTRICAS & CONTABILIDAD (ADMIN DASHBOARD)
	// ==========================================
	mux.HandleFunc("/api/admin/metrics", func(w http.ResponseWriter, r *http.Request) {
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
		})
	})

	// Gastos
	mux.HandleFunc("/api/admin/expenses", func(w http.ResponseWriter, r *http.Request) {
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
			json.NewEncoder(w).Encode(newExp)
			return
		}

		store.mu.RLock()
		defer store.mu.RUnlock()
		json.NewEncoder(w).Encode(store.expenses)
	})

	// Purchase Orders
	mux.HandleFunc("/api/admin/purchase-orders", func(w http.ResponseWriter, r *http.Request) {
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
		json.NewEncoder(w).Encode(store.purchaseOrders)
	})

	port := 8080
	fmt.Printf("\n======================================================\n")
	fmt.Printf("🚀 SERVIDOR KIDO DIECAST & COLLECTIBLES (ECOMMERCE & ADMIN)\n")
	fmt.Printf("👉 Tienda Frontend:     http://localhost:%d/\n", port)
	fmt.Printf("👉 Panel Administración: http://localhost:%d/admin.html\n", port)
	fmt.Printf("👉 API Productos:       http://localhost:%d/api/products\n", port)
	fmt.Printf("👉 API Rankings:        http://localhost:%d/api/rankings\n", port)
	fmt.Printf("👉 API Rifas & Sorteos: http://localhost:%d/api/raffles\n", port)
	fmt.Printf("======================================================\n\n")

	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), mux); err != nil {
		log.Fatalf("Error iniciando servidor: %v", err)
	}
}
