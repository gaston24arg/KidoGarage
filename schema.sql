-- =====================================================================
-- KIDO DIECAST & COLLECTIBLES - PostgreSQL Database Schema Completo
-- Sistema E-commerce + Contaduría, Stock, Rifas, Pagos Parciales & Gamificación
-- =====================================================================

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- 1. TABLA DE USUARIOS (Clientes y Administradores)
CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    email VARCHAR(200) UNIQUE NOT NULL, -- Correo como username
    password_hash VARCHAR(255),
    role VARCHAR(20) NOT NULL DEFAULT 'CLIENT', -- 'ADMIN' o 'CLIENT'
    google_id VARCHAR(150),
    first_name VARCHAR(100),
    last_name VARCHAR(100),
    phone VARCHAR(50),
    locality VARCHAR(100), -- Localidad
    street VARCHAR(150),   -- Calle
    street_number VARCHAR(50), -- Altura
    avatar_url TEXT,
    
    -- Gamificación y Cliente Frecuente
    consecutive_months_buying INT NOT NULL DEFAULT 0, -- Meses consecutivos con compra
    is_frequent_customer BOOLEAN NOT NULL DEFAULT FALSE, -- Verdadero si > 3 meses seguidos
    frequent_points INT NOT NULL DEFAULT 0, -- 1 punto por cada mes extra después de los 3 meses
    total_purchases_count INT NOT NULL DEFAULT 0, -- 1 punto por cada compra total
    has_claimed_free_raffle_current BOOLEAN DEFAULT FALSE, -- Número de rifa gratis por sorteo
    
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_frequent ON users(is_frequent_customer);

-- 2. TABLA DE ARTÍCULOS / PRODUCTOS
CREATE TABLE IF NOT EXISTS products (
    id BIGSERIAL PRIMARY KEY,
    title VARCHAR(255) NOT NULL,
    handle VARCHAR(255) UNIQUE NOT NULL,
    product_type VARCHAR(50) NOT NULL, -- 'Autito', 'Remera', 'Sticker'
    
    -- Campos condicionales según tipo
    scale VARCHAR(50), -- Obligatorio si 'Autito' (1:64, 1:43, 1:18, etc.)
    apparel_size VARCHAR(50), -- Obligatorio si 'Remera' (S, M, L, XL, XXL, Oversize)
    
    vendor VARCHAR(100) NOT NULL, -- Marca: Kaido House, Pop Race, Mini GT, Hot Wheels, etc.
    price_ars NUMERIC(12, 2) NOT NULL,
    price_usd NUMERIC(10, 2) NOT NULL DEFAULT 0.00,
    stock_quantity INT NOT NULL DEFAULT 0,
    status VARCHAR(50) NOT NULL DEFAULT 'STOCK', -- 'STOCK', 'PRE_VENTA', 'AGOTADO'
    is_active BOOLEAN NOT NULL DEFAULT TRUE, -- Activo Sí/No
    
    has_chase_chance BOOLEAN DEFAULT FALSE,
    gallery_images JSONB DEFAULT '[]'::jsonb, -- Galería de múltiples fotos
    short_video_url TEXT, -- Video muy corto del artículo
    
    description TEXT,
    tags TEXT[],
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_products_type ON products(product_type);
CREATE INDEX IF NOT EXISTS idx_products_vendor ON products(vendor);
CREATE INDEX IF NOT EXISTS idx_products_status ON products(status);
CREATE INDEX IF NOT EXISTS idx_products_active ON products(is_active);

-- 3. HISTORIAL DE MOVIMIENTOS Y CARGA MASIVA DE STOCK
CREATE TABLE IF NOT EXISTS stock_movements (
    id BIGSERIAL PRIMARY KEY,
    product_id BIGINT REFERENCES products(id) ON DELETE CASCADE,
    change_amount INT NOT NULL, -- Cantidad sumada o restada
    previous_stock INT NOT NULL,
    new_stock INT NOT NULL,
    reason VARCHAR(100) NOT NULL, -- 'CARGA_MASIVA', 'VENTA_ECOMMERCE', 'AJUSTE_INVENTARIO'
    admin_user_id BIGINT REFERENCES users(id),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 4. TABLA DE PEDIDOS Y CONTROL DE ENTREGAS
CREATE TABLE IF NOT EXISTS orders (
    id BIGSERIAL PRIMARY KEY,
    order_number VARCHAR(50) UNIQUE NOT NULL,
    customer_id BIGINT REFERENCES users(id),
    total_ars NUMERIC(12, 2) NOT NULL,
    total_paid_ars NUMERIC(12, 2) NOT NULL DEFAULT 0.00,
    remaining_balance_ars NUMERIC(12, 2) NOT NULL, -- Saldo pendiente
    is_fully_paid BOOLEAN NOT NULL DEFAULT FALSE,
    delivery_status VARCHAR(50) NOT NULL DEFAULT 'BLOQUEADO_POR_SALDO', -- 'BLOQUEADO_POR_SALDO', 'LISTO_PARA_DESPACHAR', 'EN_CAMINO', 'ENTREGADO'
    order_type VARCHAR(50) NOT NULL DEFAULT 'VENTA_DIRECTA', -- 'VENTA_DIRECTA', 'PRE_VENTA_CON_SEÑA'
    notes TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 5. ITEMS DEL PEDIDO
CREATE TABLE IF NOT EXISTS order_items (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT REFERENCES orders(id) ON DELETE CASCADE,
    product_id BIGINT REFERENCES products(id),
    product_title VARCHAR(255) NOT NULL,
    quantity INT NOT NULL DEFAULT 1,
    unit_price_ars NUMERIC(12, 2) NOT NULL,
    subtotal_ars NUMERIC(12, 2) NOT NULL
);

-- 6. CONTROL DE PAGOS PARCIALES (MERCADO PAGO)
CREATE TABLE IF NOT EXISTS partial_payments (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT REFERENCES orders(id) ON DELETE CASCADE,
    product_id BIGINT REFERENCES products(id),
    amount_ars NUMERIC(12, 2) NOT NULL,
    payment_method VARCHAR(50) DEFAULT 'Mercado Pago',
    mercadopago_payment_id VARCHAR(100),
    mercadopago_status VARCHAR(50) DEFAULT 'approved', -- 'approved', 'pending', 'rejected'
    is_downpayment BOOLEAN DEFAULT FALSE, -- Si es pago de seña/reserva
    receipt_url TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 7. TABLA DE RIFAS Y SORTEOS
CREATE TABLE IF NOT EXISTS raffles (
    id BIGSERIAL PRIMARY KEY,
    raffle_number VARCHAR(50) UNIQUE NOT NULL, -- Número o código del sorteo (ej: "RIFA-#01-CHASE-R34")
    title VARCHAR(255) NOT NULL,
    prize_description TEXT NOT NULL,
    prize_images JSONB DEFAULT '[]'::jsonb, -- Fotos del premio
    start_datetime TIMESTAMP WITH TIME ZONE NOT NULL, -- Inicio de venta
    end_datetime TIMESTAMP WITH TIME ZONE NOT NULL,   -- Fin de venta
    draw_datetime TIMESTAMP WITH TIME ZONE NOT NULL,  -- Fecha del sorteo oficial
    min_number INT NOT NULL DEFAULT 0, -- Ej: 00
    max_number INT NOT NULL DEFAULT 99, -- Ej: 99 (100 números)
    ticket_price_ars NUMERIC(10, 2) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'ACTIVA', -- 'ACTIVA', 'FINALIZADA', 'SORTEADA', 'CANCELADA'
    winner_ticket_number INT,
    winner_customer_id BIGINT REFERENCES users(id),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 8. NÚMEROS / TICKETS VENDIDOS DE CADA RIFA
CREATE TABLE IF NOT EXISTS raffle_tickets (
    id BIGSERIAL PRIMARY KEY,
    raffle_id BIGINT REFERENCES raffles(id) ON DELETE CASCADE,
    ticket_number INT NOT NULL,
    customer_id BIGINT REFERENCES users(id),
    customer_email VARCHAR(200) NOT NULL,
    is_free_frequent_ticket BOOLEAN DEFAULT FALSE, -- Número de rifa gratis por ser cliente frecuente
    mercadopago_id VARCHAR(100),
    purchased_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT unique_ticket_per_raffle UNIQUE (raffle_id, ticket_number)
);

-- 9. GASTOS CONTABLES
CREATE TABLE IF NOT EXISTS expenses (
    id BIGSERIAL PRIMARY KEY,
    category VARCHAR(100) NOT NULL, -- 'Flete Internacional', 'Aduana / Impuestos', 'Packaging Coleccionista', 'Publicidad', 'Servicios'
    concept VARCHAR(255) NOT NULL,
    supplier VARCHAR(150) NOT NULL,
    amount_usd NUMERIC(12, 2) DEFAULT 0.00,
    amount_ars NUMERIC(14, 2) NOT NULL,
    invoice_number VARCHAR(100),
    expense_date DATE NOT NULL DEFAULT CURRENT_DATE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 10. ÓRDENES DE COMPRA A PROVEEDORES
CREATE TABLE IF NOT EXISTS purchase_orders (
    id BIGSERIAL PRIMARY KEY,
    po_number VARCHAR(50) UNIQUE NOT NULL,
    supplier_name VARCHAR(150) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'BORRADOR', -- 'BORRADOR', 'EMITIDA', 'PAGADA', 'EN_TRANSITO', 'RECIBIDA'
    order_date DATE NOT NULL DEFAULT CURRENT_DATE,
    expected_delivery_date DATE,
    total_usd NUMERIC(12, 2) NOT NULL DEFAULT 0.00,
    tracking_number VARCHAR(100),
    notes TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);
