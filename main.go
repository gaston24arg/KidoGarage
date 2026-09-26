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

	_ "github.com/lib/pq"
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
	DeliveryStatus      string           `json:"delivery_status"` // "BLOQUEADO_POR_SALDO", "LISTO_PARA_DESPACHAR", "EN_CAMINO", "ENTREGADO"
	OrderType           string           `json:"order_type"` // "VENTA_DIRECTA" o "PRE_VENTA_CON_SEÑA"
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

	// Boletos para Martín (Cliente Frecuente) en la rifa activa
	sampleRaffle.Tickets[7] = RaffleTicket{
		Number:        7,
		CustomerID:    2,
		CustomerEmail: "martin@kido.com.ar",
		CustomerName:  "Martín Gómez",
		IsFreeTicket:  true, // Boleto gratis de socio frecuente
		MercadoPagoID: "MP-FREQUENT-FREE-07",
		PurchasedAt:   time.Now().Add(-24 * time.Hour),
	}
	sampleRaffle.Tickets[24] = RaffleTicket{
		Number:        24,
		CustomerID:    2,
		CustomerEmail: "martin@kido.com.ar",
		CustomerName:  "Martín Gómez",
		IsFreeTicket:  false,
		MercadoPagoID: "MP-TICKET-BUY-24",
		PurchasedAt:   time.Now().Add(-12 * time.Hour),
	}

	// Rifa 2: Sorteada (para comprobar ganador)
	winningNum := 42
	pastRaffle := Raffle{
		ID:               2,
		RaffleNumber:     "RIFA-#00-SUPRA-MK4",
		Title:            "Inno64 1:64 Toyota Supra MK4 Castrol JGTC Special Edition",
		PrizeDescription: "Edición especial de colección con vitrina de acrílico y calcas oficiales JGTC.",
		PrizeImages: []string{
			"https://cdn.shopify.com/s/files/1/0978/6929/9988/files/IMG_7095.jpg?v=1790150116",
		},
		StartDatetime:  "2026-08-01 10:00",
		EndDatetime:    "2026-08-30 20:00",
		DrawDatetime:   "2026-08-31 21:00 (Sorteo Oficial)",
		MinNumber:      0,
		MaxNumber:      99,
		TicketPriceARS: 2000,
		Status:         "SORTEADA",
		Tickets:        make(map[int]RaffleTicket),
		WinnerNumber:   &winningNum,
	}
	pastRaffle.Tickets[42] = RaffleTicket{
		Number:        42,
		CustomerID:    2,
		CustomerEmail: "martin@kido.com.ar",
		CustomerName:  "Martín Gómez",
		IsFreeTicket:  false,
		MercadoPagoID: "MP-TICKET-PAST-42",
		PurchasedAt:   time.Now().Add(-720 * time.Hour),
	}

	store.raffles = []Raffle{sampleRaffle, pastRaffle}
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
			ShippingMethod:      "Correo Argentino - Envío a Domicilio (Clásico)",
			ShippingCostARS:     5200,
			ShippingPostalCode:  "1425",
			ShippingAddress:     "Av. Cabildo 2450, Piso 4 B, CABA",
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
			ShippingMethod:      "Retiro Oficial en KIDO Garage (Villa Urquiza, CABA)",
			ShippingCostARS:     0,
			ShippingPostalCode:  "1430",
			ShippingAddress:     "Punto de Retiro KIDO Showroom",
			Items: []OrderItem{
				{ProductID: 999001, ProductTitle: "Pop Race Mazda RX-7 RE-Amemiya Chrome", Quantity: 1, UnitPriceARS: 18500, SubtotalARS: 18500, ProductType: "Autito", ScaleOrSize: "1:64"},
			},
			Payments: []PartialPayment{
				{ID: 2, OrderID: 1002, ProductID: 999001, AmountARS: 18500, PaymentMethod: "Mercado Pago", MercadoPagoID: "MP-FULL-8812", PaymentStatus: "approved", IsDownPayment: false, CreatedAt: time.Now().Add(-2 * 24 * time.Hour)},
			},
			CreatedAt: time.Now().Add(-2 * 24 * time.Hour),
		},
		{
			ID:                  1003,
			OrderNumber:         "KIDO-ORD-1003",
			CustomerID:          2, // Martín
			CustomerEmail:       "martin@kido.com.ar",
			CustomerName:        "Martín Gómez",
			TotalARS:            38500,
			TotalPaidARS:        38500,
			RemainingBalanceARS: 0,
			IsFullyPaid:         true,
			DeliveryStatus:      "EN_CAMINO", // Paquete en tránsito con código de seguimiento
			OrderType:           "VENTA_DIRECTA",
			ShippingMethod:      "Correo Argentino - Envío a Domicilio (Clásico)",
			ShippingCostARS:     5200,
			ShippingPostalCode:  "1425",
			ShippingAddress:     "Av. Cabildo 2450, Piso 4 B, CABA",
			TrackingNumber:      "AR-CORREO-9481827",
			TrackingCarrier:     "Correo Argentino",
			Items: []OrderItem{
				{ProductID: 10390924034324, ProductTitle: "Kaido House Datsun 510 Pro Street BRE", Quantity: 1, UnitPriceARS: 38500, SubtotalARS: 38500, ProductType: "Autito", ScaleOrSize: "1:64"},
			},
			Payments: []PartialPayment{
				{ID: 3, OrderID: 1003, ProductID: 10390924034324, AmountARS: 38500, PaymentMethod: "Mercado Pago", MercadoPagoID: "MP-FULL-9931", PaymentStatus: "approved", IsDownPayment: false, CreatedAt: time.Now().Add(-8 * 24 * time.Hour)},
			},
			CreatedAt: time.Now().Add(-8 * 24 * time.Hour),
		},
		{
			ID:                  1004,
			OrderNumber:         "KIDO-ORD-1004",
			CustomerID:          2, // Martín
			CustomerEmail:       "martin@kido.com.ar",
			CustomerName:        "Martín Gómez",
			TotalARS:            24000,
			TotalPaidARS:        24000,
			RemainingBalanceARS: 0,
			IsFullyPaid:         true,
			DeliveryStatus:      "ENTREGADO", // Paquete entregado
			OrderType:           "VENTA_DIRECTA",
			ShippingMethod:      "Andreani Express - Puerta a Puerta Prioritario",
			ShippingCostARS:     6900,
			ShippingPostalCode:  "1425",
			ShippingAddress:     "Av. Cabildo 2450, Piso 4 B, CABA",
			TrackingNumber:      "ADR-9048123",
			TrackingCarrier:     "Andreani",
			Items: []OrderItem{
				{ProductID: 10390924034324, ProductTitle: "Remera Oversized KIDO Touge Legends Negra", Quantity: 1, UnitPriceARS: 24000, SubtotalARS: 24000, ProductType: "Remera", ScaleOrSize: "L"},
			},
			Payments: []PartialPayment{
				{ID: 4, OrderID: 1004, ProductID: 10390924034324, AmountARS: 24000, PaymentMethod: "Mercado Pago", MercadoPagoID: "MP-FULL-7711", PaymentStatus: "approved", IsDownPayment: false, CreatedAt: time.Now().Add(-30 * 24 * time.Hour)},
			},
			CreatedAt: time.Now().Add(-30 * 24 * time.Hour),
		},
	}
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
// CONEXIÓN & PERSISTENCIA EN POSTGRESQL
// ==========================================

var (
	db       *sql.DB
	dbActive bool
)

func initDB() {
	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		connStr = "postgres://postgres:715192@localhost:5432/kido_db?sslmode=disable"
	}

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Printf("⚠️ PostgreSQL: No se pudo crear el pool de conexiones: %v", err)
		return
	}

	if err := db.Ping(); err != nil {
		log.Printf("⚠️ PostgreSQL: No se pudo conectar a localhost:5432/kido_db: %v", err)
		return
	}

	dbActive = true
	log.Printf("🐘 PostgreSQL CONECTADO EXITOSAMENTE a kido_db en localhost:5432")
	_, _ = db.Exec("ALTER TABLE raffles ADD COLUMN IF NOT EXISTS winner_number INTEGER")
	_, _ = db.Exec(`ALTER TABLE orders 
		ADD COLUMN IF NOT EXISTS shipping_method VARCHAR(100),
		ADD COLUMN IF NOT EXISTS shipping_cost_ars INTEGER DEFAULT 0,
		ADD COLUMN IF NOT EXISTS shipping_postal_code VARCHAR(20),
		ADD COLUMN IF NOT EXISTS shipping_address TEXT,
		ADD COLUMN IF NOT EXISTS tracking_number VARCHAR(100),
		ADD COLUMN IF NOT EXISTS tracking_carrier VARCHAR(50)`)

	// 1. Sincronizar usuarios
	var userCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err == nil {
		if userCount == 0 {
			store.mu.RLock()
			for _, u := range store.users {
				_, _ = db.Exec(`INSERT INTO users (id, email, password_hash, role, first_name, last_name, phone, locality, street, street_number, avatar_url, consecutive_months_buying, is_frequent_customer, frequent_points, total_purchases_count)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
				ON CONFLICT (id) DO NOTHING`,
					u.ID, u.Email, u.Password, u.Role, u.FirstName, u.LastName, u.Phone, u.Locality, u.Street, u.StreetNumber, u.AvatarURL, u.ConsecutiveMonths, u.IsFrequentCustomer, u.FrequentPoints, u.TotalPurchasesCount)
			}
			store.mu.RUnlock()
			log.Printf("🐘 [PostgreSQL] Sembrados %d usuarios en tabla 'users'", len(store.users))
		} else {
			rows, err := db.Query("SELECT id, email, COALESCE(password_hash,''), role, COALESCE(first_name,''), COALESCE(last_name,''), COALESCE(phone,''), COALESCE(locality,''), COALESCE(street,''), COALESCE(street_number,''), COALESCE(avatar_url,''), consecutive_months_buying, is_frequent_customer, frequent_points, total_purchases_count FROM users ORDER BY id ASC")
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
	}

	// 2. Sincronizar catálogo de productos
	var prodCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM products").Scan(&prodCount); err == nil {
		if prodCount == 0 {
			store.mu.RLock()
			for _, p := range store.products {
				imgJSON, _ := json.Marshal(p.GalleryImages)
				_, _ = db.Exec(`INSERT INTO products (id, title, handle, product_type, scale, apparel_size, vendor, price_ars, price_usd, stock_quantity, status, is_active, has_chase_chance, gallery_images, short_video_url, description)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
				ON CONFLICT (id) DO NOTHING`,
					p.ID, p.Title, p.Handle, p.ProductType, p.Scale, p.ApparelSize, p.Vendor, p.PriceARS, p.PriceUSD, p.StockQuantity, p.Status, p.IsActive, p.HasChaseChance, string(imgJSON), p.ShortVideoURL, p.BodyHTML)
			}
			store.mu.RUnlock()
			log.Printf("🐘 [PostgreSQL] Sembrados %d productos en tabla 'products'", len(store.products))
		} else {
			rows, err := db.Query("SELECT id, title, handle, product_type, COALESCE(scale,''), COALESCE(apparel_size,''), vendor, price_ars, price_usd, stock_quantity, status, is_active, has_chase_chance, COALESCE(gallery_images::text,'[]'), COALESCE(short_video_url,''), COALESCE(description,'') FROM products ORDER BY id ASC")
			if err == nil {
				var pgProducts []Product
				for rows.Next() {
					var p Product
					var imgRaw string
					if err := rows.Scan(&p.ID, &p.Title, &p.Handle, &p.ProductType, &p.Scale, &p.ApparelSize, &p.Vendor, &p.PriceARS, &p.PriceUSD, &p.StockQuantity, &p.Status, &p.IsActive, &p.HasChaseChance, &imgRaw, &p.ShortVideoURL, &p.BodyHTML); err == nil {
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
}

func pgSaveUser(u User) {
	if !dbActive || db == nil {
		return
	}
	go func() {
		_, err := db.Exec(`INSERT INTO users (id, email, password_hash, role, first_name, last_name, phone, locality, street, street_number, avatar_url, consecutive_months_buying, is_frequent_customer, frequent_points, total_purchases_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (id) DO UPDATE SET
			email = EXCLUDED.email,
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
			u.ID, u.Email, u.Password, u.Role, u.FirstName, u.LastName, u.Phone, u.Locality, u.Street, u.StreetNumber, u.AvatarURL, u.ConsecutiveMonths, u.IsFrequentCustomer, u.FrequentPoints, u.TotalPurchasesCount)
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
		_, err := db.Exec(`INSERT INTO products (id, title, handle, product_type, scale, apparel_size, vendor, price_ars, price_usd, stock_quantity, status, is_active, has_chase_chance, gallery_images, short_video_url, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (id) DO UPDATE SET
			stock_quantity = EXCLUDED.stock_quantity,
			status = EXCLUDED.status,
			is_active = EXCLUDED.is_active,
			price_ars = EXCLUDED.price_ars,
			price_usd = EXCLUDED.price_usd,
			updated_at = CURRENT_TIMESTAMP`,
			p.ID, p.Title, p.Handle, p.ProductType, p.Scale, p.ApparelSize, p.Vendor, p.PriceARS, p.PriceUSD, p.StockQuantity, p.Status, p.IsActive, p.HasChaseChance, string(imgJSON), p.ShortVideoURL, p.BodyHTML)
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
	loadCatalog()
	initRaffles()
	initOrders()
	initDB()
	loadMPConfig()

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
		pgSaveUser(newUser)

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
		pgSaveProduct(p)

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
		pgSaveRaffle(newRaffle)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"raffle":  newRaffle,
			"message": "Rifa creada y abierta al público",
		})
	})

	// Datos en Vivo para Ruleta / Sorteo en Vivo (Streams & OBS)
	mux.HandleFunc("/api/admin/raffles/live-data", func(w http.ResponseWriter, r *http.Request) {
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
	})

	// Ejecutar Sorteo y Registrar Ganador Oficial
	mux.HandleFunc("/api/admin/raffles/draw", func(w http.ResponseWriter, r *http.Request) {
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
				"number":        chosenNumber,
				"name":          ticket.CustomerName,
				"email":         ticket.CustomerEmail,
				"is_free":       ticket.IsFreeTicket,
				"mercadopago_id": ticket.MercadoPagoID,
			},
			"raffle": target,
		})
	})

	// Reiniciar Rifa a Estado Activa (Para Pruebas y Streams)
	mux.HandleFunc("/api/admin/raffles/reset", func(w http.ResponseWriter, r *http.Request) {
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
		pgSaveRaffleTicket(targetRaffle.ID, targetRaffle.Tickets[req.Number])

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
					"success":                true,
					"order_id":               ord.ID,
					"amount_paid":            req.AmountARS,
					"remaining_balance_ars":  ord.RemainingBalanceARS,
					"is_fully_paid":          ord.IsFullyPaid,
					"delivery_status":        ord.DeliveryStatus,
					"init_point":             initPoint,
					"sandbox_init_point":     sandboxInitPoint,
					"preference_id":          prefID,
					"is_real_mp":             mpConfig.AccessToken != "",
					"message":                "Pago de saldo procesado exitosamente.",
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
			"db_status":                 "PostgreSQL kido_db (localhost:5432)",
			"db_active":                 dbActive,
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
			pgSaveExpense(newExp)
			json.NewEncoder(w).Encode(newExp)
			return
		}

		store.mu.RLock()
		defer store.mu.RUnlock()
		json.NewEncoder(w).Encode(store.expenses)
	})

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
	mux.HandleFunc("/api/admin/mercadopago/config", func(w http.ResponseWriter, r *http.Request) {
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
				"success":      true,
				"message":      "Credenciales de Mercado Pago guardadas y validadas exitosamente",
				"is_sandbox":   mpConfig.IsSandbox,
				"is_configured": mpConfig.AccessToken != "",
				"masked_token": maskToken(mpConfig.AccessToken),
				"account_info": accountInfo,
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
