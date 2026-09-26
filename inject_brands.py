with open('public/admin.html', 'r', encoding='utf-8') as f:
    html = f.read()

btn_target = """      <button onclick="switchTab('raffles')" id="tabBtn-raffles\""""
btn_insert = """      <button onclick="switchTab('brands')" id="tabBtn-brands" class="tab-btn px-4 py-3 md:w-full rounded-xl bg-brandSurface text-gray-400 hover:text-white transition flex items-center gap-3 whitespace-nowrap border border-transparent hover:border-brandBorder text-xs font-semibold text-left">
        <span class="text-lg">✨</span> Marcas & Carrusel
      </button>
"""

if btn_target in html and 'tabBtn-brands' not in html:
    html = html.replace(btn_target, btn_insert + btn_target)

tab_target = """    <!-- ======================================================== -->
    <!-- TAB 4:"""
tab_insert = """    <!-- ======================================================== -->
    <!-- TAB 4B: MARCAS Y CARRUSEL                                -->
    <!-- ======================================================== -->
    <section id="tab-brands" class="tab-pane hidden">
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div class="bg-brandSurface border border-brandBorder rounded-2xl p-5 shadow-lg h-fit">
          <h3 class="font-bold text-white text-base mb-4 font-display">Nueva Marca</h3>
          <form id="brandForm" class="space-y-4" onsubmit="saveBrand(event)">
            <input type="hidden" id="brandId" value="0">
            <div>
              <label class="block text-xs text-gray-400 mb-1">Nombre de la Marca</label>
              <input required id="brandName" type="text" class="w-full bg-brandCard border border-brandBorder rounded-xl p-2.5 text-white text-sm focus:border-brandAccent focus:outline-none">
            </div>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Código Interno</label>
              <input required id="brandCode" type="text" class="w-full bg-brandCard border border-brandBorder rounded-xl p-2.5 text-white text-sm focus:border-brandAccent focus:outline-none">
            </div>
            <div>
              <label class="flex items-center gap-2 cursor-pointer text-gray-300 text-sm">
                <input type="checkbox" id="brandCarrusel" class="rounded bg-brandCard border-brandBorder text-brandAccent"> Mostrar en Carrusel Animado
              </label>
            </div>
            <div>
              <label class="block text-xs text-gray-400 mb-1">Imagen / Logo para Carrusel</label>
              <input id="brandImageFile" type="file" accept="image/*" class="w-full bg-brandCard border border-brandBorder rounded-xl p-2 text-white text-xs mb-2">
              <input type="hidden" id="brandImageUrl" value="">
              <div id="brandImagePreview" class="hidden">
                <img id="brandPreviewImg" class="h-12 object-contain bg-brandDark p-1 border border-brandBorder rounded-lg">
              </div>
            </div>
            <button type="submit" class="w-full bg-brandAccent hover:bg-brandAccentHover text-white py-2.5 rounded-xl font-bold uppercase tracking-wider text-xs transition">
              Guardar Marca
            </button>
            <button type="button" onclick="resetBrandForm()" class="w-full bg-brandSurface hover:bg-brandBorder border border-brandBorder text-gray-300 py-2 rounded-xl text-xs font-semibold transition mt-2">
              Limpiar Formulario
            </button>
          </form>
        </div>
        
        <div class="lg:col-span-2 bg-brandSurface border border-brandBorder rounded-2xl p-5 shadow-lg">
          <h3 class="font-bold text-white text-base mb-4 font-display">Directorio de Marcas</h3>
          <div class="overflow-x-auto">
            <table class="w-full text-left text-xs text-gray-300">
              <thead class="bg-brandDark/50 text-gray-500 uppercase">
                <tr>
                  <th class="p-3 rounded-l-xl">ID</th>
                  <th class="p-3">Marca</th>
                  <th class="p-3 text-center">En Carrusel</th>
                  <th class="p-3 rounded-r-xl">Acciones</th>
                </tr>
              </thead>
              <tbody id="brandsTableBody" class="divide-y divide-brandBorder">
                <!-- JS render -->
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </section>

"""

if tab_target in html and 'tab-brands' not in html:
    html = html.replace(tab_target, tab_insert + tab_target)

with open('public/admin.html', 'w', encoding='utf-8') as f:
    f.write(html)
