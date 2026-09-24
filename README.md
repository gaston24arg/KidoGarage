# KIDO Garage - E-commerce & Sistema de Gestión

Plataforma completa de comercio electrónico y gestión integral para **KIDO Garage**: venta de autitos coleccionables Die-Cast (1:64, 1:18, 1:43, marcas como Kaido House, Pop Race, Mini GT, Spark, Tarmac Works, Hot Wheels, Ignition Model), indumentaria JDM (remeras y hoodies) y stickers vinilo.

## 🚀 Características

### Tienda E-Commerce (`public/index.html`)
- **Diseño Dark Theme & Identidad JDM:** Estética moderna, rápida y adaptable con tipografía personalizada *Fuji Quake Zone* para la marca.
- **Carrusel Hero:** Artículos destacados con visualización de Chase 1/24 y reserva inmediata.
- **Carrusel de Marcas:** Navegación rápida por fabricantes (Kaido House, Pop Race, Mini GT, Spark, etc.).
- **Catálogo & Filtros Dinámicos:** Búsqueda en tiempo real, filtro por estado (Stock, Pre-Venta, Agotado), escala (1:64, 1:18) y tipo de producto.
- **Sorteos & Rifas Transparentes:** Cuadrícula interactiva de números (0 al 99), compra directa y beneficio de **1 número gratis** para clientes frecuentes.
- **Club de Clientes Frecuentes & Rankings:** Gamificación de clientes con compras consecutivas (>3 meses) y ranking por compras totales.
- **Carrito & Pagos Parciales:** Integración de Mercado Pago con soporte para pago total (100%) o pago de seña/reserva (30%) para congelar precio en pre-ventas.
- **Atención Directa:** Botón flotante y enlaces directos de WhatsApp pre-configurados.

### Panel de Administración (`public/admin.html`)
- **Métricas en Tiempo Real:** Ventas totales, efectivo cobrado, saldos a cobrar, unidades en stock y gastos operativos.
- **Control de Stock & Grilla:** Ajuste rápido de unidades y alertas de stock bajo/agotado.
- **Carga Masiva de Stock:** Suma ágil de inventario por lote.
- **Alta de Artículos con Campos Condicionales:**
  - Si es *Autito*: requiere selección obligatoria de Escala (1:64, 1:18, etc.).
  - Si es *Remera*: requiere selección obligatoria de Talle (S, M, L, XL, XXL).
- **Emisión de Rifas:** Creación de sorteos con rango de números y precio por ticket.
- **Control de Pagos & Despacho Seguro:** Bloqueo automático de entregas en pedidos con saldo pendiente y registro de cobranza parcial.
- **Contaduría & Gastos Operativos:** Libro de egresos categorizados (fletes, aduana, packaging).
- **Gestión de Clientes:** Otorgamiento y revocación manual de la insignia *⭐ Cliente Frecuente* desde la grilla.

## 🛠️ Tecnologías

- **Backend:** Go (Golang) estándar, sin dependencias externas pesadas, arquitectura RESTful.
- **Frontend:** HTML5 semántico, Tailwind CSS, JavaScript Vanilla, Google Fonts (Outfit, Plus Jakarta Sans) y Fuji Quake Zone.
- **Base de Datos:** Estructura documentada en `schema.sql` (PostgreSQL / SQLite).

## 🏁 Cómo Ejecutar Localmente

1. Clonar el repositorio.
2. Iniciar el servidor Go:
   ```bash
   go run main.go
   ```
3. Acceder en el navegador:
   - **Tienda:** [http://localhost:8080/](http://localhost:8080/)
   - **Panel Admin:** [http://localhost:8080/admin.html](http://localhost:8080/admin.html)
