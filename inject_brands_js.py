with open('public/admin.html', 'r', encoding='utf-8') as f:
    html = f.read()

js_target = """    async function loadBrands() {"""
js_insert = """
    function renderBrandsAdminTable() {
      const tbody = document.getElementById('brandsTableBody');
      if (!tbody) return;
      if (!adminBrands || adminBrands.length === 0) {
        tbody.innerHTML = '<tr><td colspan="4" class="p-3 text-center text-gray-500 italic">No hay marcas registradas.</td></tr>';
        return;
      }
      tbody.innerHTML = adminBrands.map(b => `
        <tr class="hover:bg-brandCard/30 transition">
          <td class="p-3">#${b.id}</td>
          <td class="p-3 font-bold text-white flex items-center gap-2">
            ${b.imagen ? `<img src="${b.imagen}" class="h-6 object-contain bg-brandCard p-0.5 rounded" />` : ''}
            ${b.name}
          </td>
          <td class="p-3 text-center">
            ${b.carrusel ? '<span class="text-green-400 font-bold">Sí</span>' : '<span class="text-gray-500">No</span>'}
          </td>
          <td class="p-3">
            <button onclick='editBrand(${JSON.stringify(b)})' class="text-blue-400 hover:text-blue-300 text-[10px] uppercase font-bold tracking-wider px-2 py-1 border border-blue-500/30 rounded bg-blue-500/10 transition">Editar</button>
          </td>
        </tr>
      `).join('');
    }

    function editBrand(b) {
      document.getElementById('brandId').value = b.id;
      document.getElementById('brandName').value = b.name;
      document.getElementById('brandCode').value = b.code;
      document.getElementById('brandCarrusel').checked = b.carrusel;
      document.getElementById('brandImageUrl').value = b.imagen || '';
      
      const previewDiv = document.getElementById('brandImagePreview');
      const previewImg = document.getElementById('brandPreviewImg');
      if (b.imagen) {
        previewImg.src = b.imagen;
        previewDiv.classList.remove('hidden');
      } else {
        previewDiv.classList.add('hidden');
      }
      
      switchTab('brands');
    }

    function resetBrandForm() {
      document.getElementById('brandId').value = '0';
      document.getElementById('brandName').value = '';
      document.getElementById('brandCode').value = '';
      document.getElementById('brandCarrusel').checked = false;
      document.getElementById('brandImageFile').value = '';
      document.getElementById('brandImageUrl').value = '';
      document.getElementById('brandImagePreview').classList.add('hidden');
    }

    async function saveBrand(e) {
      e.preventDefault();
      
      let imageUrl = document.getElementById('brandImageUrl').value;
      const fileInput = document.getElementById('brandImageFile');
      
      if (fileInput.files.length > 0) {
        const formData = new FormData();
        formData.append('file', fileInput.files[0]);
        try {
          const uploadRes = await fetch('/api/admin/upload', {
            method: 'POST',
            body: formData
          });
          if (uploadRes.ok) {
            const data = await uploadRes.json();
            imageUrl = data.url;
          } else {
            alert('Error al subir la imagen');
            return;
          }
        } catch (err) {
          console.error('Error subiendo imagen:', err);
          alert('Error de conexión al subir la imagen.');
          return;
        }
      }

      const b = {
        id: parseInt(document.getElementById('brandId').value) || 0,
        name: document.getElementById('brandName').value,
        code: document.getElementById('brandCode').value,
        carrusel: document.getElementById('brandCarrusel').checked,
        imagen: imageUrl
      };

      try {
        const res = await fetch('/api/admin/brands', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(b)
        });
        
        if (res.ok) {
          alert('Marca guardada correctamente.');
          resetBrandForm();
          await loadBrands(); // Recargar
        } else {
          alert('Error al guardar la marca');
        }
      } catch (err) {
        console.error(err);
        alert('Error de conexión.');
      }
    }

"""

if js_target in html and 'renderBrandsAdminTable' not in html:
    html = html.replace(js_target, js_insert + js_target)

# En la función loadBrands también debemos llamar a renderBrandsAdminTable()
load_brands_target = """          if (sel) {
            sel.innerHTML = '<option value="">Seleccione una marca...</option>' + 
                            adminBrands.map(b => `<option value="${b.id}">${b.name} (${b.code})</option>`).join('');
          }"""
load_brands_insert = """
          renderBrandsAdminTable();"""

if load_brands_target in html and 'renderBrandsAdminTable();' not in html:
    html = html.replace(load_brands_target, load_brands_target + load_brands_insert)

with open('public/admin.html', 'w', encoding='utf-8') as f:
    f.write(html)
