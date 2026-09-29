-- ========================================================
-- Script de Limpieza / Truncado de Base de Datos KIDO Garage
-- Trunca todas las tablas excepto la de usuarios ('users')
-- Reinicia las secuencias de IDs autonuméricas (RESTART IDENTITY)
-- ========================================================

TRUNCATE TABLE 
    partial_payments,
    order_items,
    orders,
    raffle_tickets,
    raffles,
    stock_movements,
    products,
    expenses,
    purchase_orders
RESTART IDENTITY CASCADE;

-- Si existen tablas de marcas o tipos creadas por migraciones:
DO $$
BEGIN
    IF EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'brands') THEN
        EXECUTE 'TRUNCATE TABLE brands RESTART IDENTITY CASCADE;';
    END IF;
    IF EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'product_types') THEN
        EXECUTE 'TRUNCATE TABLE product_types RESTART IDENTITY CASCADE;';
    END IF;
END $$;
