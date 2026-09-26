import sys

with open('public/index.html', 'r', encoding='utf-8') as f:
    lines = f.readlines()

out = []
sidebar_lines = []
in_sidebar = False
for i, line in enumerate(lines):
    if '<!-- SIDEBAR DE FILTROS (Izquierda) -->' in line:
        in_sidebar = True
    
    if in_sidebar:
        sidebar_lines.append(line)
        if '</aside>' in line:
            in_sidebar = False
    
# Now, find the start of section 3
start_sec3 = -1
for i, line in enumerate(lines):
    if '<!-- 3. CARRUSEL DE PRODUCTOS DESTACADOS -->' in line:
        start_sec3 = i - 1
        break

# Find the end of section 6
end_sec6 = -1
for i, line in enumerate(lines):
    if '</main>' in line and i > start_sec3:
        end_sec6 = i
        break

# Extract the sections
sections = lines[start_sec3:end_sec6+1]

# Remove the sidebar and its wrapper from sections
clean_sections = []
in_sidebar_remove = False
for line in sections:
    if '<main id="catalogo"' in line:
        # replace with simple <main id="catalogo">
        clean_sections.append('  <main id="catalogo" class="w-full mt-4">\n')
        continue

    if '<!-- SIDEBAR DE FILTROS (Izquierda) -->' in line:
        in_sidebar_remove = True
    
    if in_sidebar_remove:
        if '</aside>' in line:
            in_sidebar_remove = False
        continue
    
    if '<!-- ÁREA DE PRODUCTOS (Derecha) -->' in line:
        continue
    if '<div class="flex-1">' in line:
        continue
    
    # We also need to remove the closing </div> of <div class="flex-1">.
    # It is right before </main>
    if '</main>' in line:
        clean_sections = clean_sections[:-1] # drop the closing </div>
        clean_sections.append('  </main>\n')
        continue

    # Clean the max-w classes from sections so they fit nicely
    if 'max-w-[1600px] mx-auto px-4 sm:px-6 lg:px-8 py-6 w-full' in line:
        line = line.replace('max-w-[1600px] mx-auto px-4 sm:px-6 lg:px-8 py-6 w-full', 'w-full py-6')
    if 'max-w-[1600px] mx-auto px-4 sm:px-6 lg:px-8 pt-6 pb-2 w-full' in line:
        line = line.replace('max-w-[1600px] mx-auto px-4 sm:px-6 lg:px-8 pt-6 pb-2 w-full', 'w-full pt-6 pb-2')

    clean_sections.append(line)


# Create the new layout
new_layout = [
    '  <div class="max-w-[1600px] mx-auto px-4 sm:px-6 lg:px-8 py-6 w-full flex-grow flex flex-col lg:flex-row gap-8">\n'
]
new_layout.extend(sidebar_lines)
new_layout.append('\n    <!-- CONTENIDO PRINCIPAL (Derecha) -->\n')
new_layout.append('    <div class="flex-1 min-w-0">\n') # min-w-0 for flex children overflow
new_layout.extend(clean_sections)
new_layout.append('    </div>\n')
new_layout.append('  </div>\n')


# Replace in original file
new_lines = lines[:start_sec3] + new_layout + lines[end_sec6+1:]

with open('public/index.html', 'w', encoding='utf-8') as f:
    f.writelines(new_lines)

print('Success')
