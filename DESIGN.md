# Diseño de Dársena

La dirección acordada es una utilidad local sobria: jerarquía mediante alineación, separadores y tipografía de sistema, con superficies discretas y acento seleccionable. Ejemplos y definiciones comparten una única lista de aplicaciones; el tipo es un atributo, no una sección de navegación.

El título principal usa 24 px, las secciones 15–16 px, los controles 14 px y los metadatos y registros al menos 12 px. En móvil los campos usan 16 px y los objetivos táctiles al menos 44 px. Se mantienen modos claro y oscuro, doce acentos cuadrados, foco visible y preferencias de movimiento reducido. Los componentes existentes usan shadcn/Base UI. El registro de operación es una rejilla compacta (número, hora y mensaje en una misma línea base, sangría francesa al ajustar).

Emil, shadcn y Rauno son inspiraciones conceptuales de oficio e interacción solicitadas; no se ha aprobado una maqueta externa ni una copia visual exacta. Los colores semánticos de éxito y error son independientes del acento. Las listas usan separadores y radios discretos de 4–6 px; los espacios principales son de 24–36 px. La interfaz mantiene estados de carga, vacío, error y foco; el movimiento es funcional y se elimina con la preferencia de movimiento reducido.

## Navegación y estructura

La barra lateral tiene tres destinos: Aplicaciones (con contador), Actividad y Diagnóstico. En escritorio se pliega a una barra mini de 56 px (iconos con tooltip y `aria-label`) con ⌘B / Ctrl+B, y la preferencia se conserva; hasta 600 px es un menú transitorio. Ajustes se abre desde el pie de la barra y está organizado en categorías (Apariencia: tema y acento; Idioma; Interfaz: mostrar u ocultar el enlace a GitHub, `darsena.githubLink`; Sistema: enlace a Diagnóstico) con un patrón de pestañas: lista vertical a la izquierda en escritorio, pestañas horizontales desplazables en móvil, flechas con activación al enfocar. Cerrar con Escape devuelve el foco al control. La paleta ⌘K / Ctrl+K busca aplicaciones, rutas y acciones (nueva aplicación, tema, idioma) con patrón combobox y devuelve el foco al cerrar.

La lista de Aplicaciones está agrupada («Ejemplos», «Sin proyecto»), preparada para que los proyectos añadan grupos. Filas de unos 48 px con franja de estado, nombre, subtítulo monoespaciado, tipo, estado y último cambio; en móvil son tarjetas compactas. Filtros por tipo y «Requieren atención», búsqueda con `/` y grupos plegables. Cada aplicación tiene un detalle con pestañas enlazables (Resumen y Despliegues en ejemplos; Resumen y Variables en definiciones) y un único «Siguiente paso» según su estado. Un borrador sin guardar avisa antes de salir. La creación empieza por elegir el tipo cuando hay más de uno.

## Estados y avisos

El vocabulario de estado es único: ok, en curso (progress), atención (attention) y reposo (idle). Se muestra siempre con icono y texto, y como franja de color (el «atraque») en el borde de la fila o la cabecera; la franja es decorativa y no sustituye al texto. Hay pocos avisos: la píldora de modo (Demo / Experimental, con su disponibilidad) enlaza a Diagnóstico, y en demo una sola línea bajo la cabecera recuerda que los despliegues son simulados. El resto del detalle de capacidades vive en Diagnóstico, con un siguiente paso (comando copiable o documentación) por cada comprobación fallida.

## Idioma y marca

La interfaz está en español e inglés. El idioma se elige solo en Ajustes, siempre se empieza en español y se guarda como el tema y el acento; los errores se traducen por estado y código, sin mostrar texto del servidor. La marca es un símbolo de pantalanes (también el favicon) sobre el acento activo.

Los estados operativos usan texto explícito además de color: la cabecera indica cuándo el estado del servidor está por comprobar o no está disponible, y las aplicaciones guardadas distinguen worker, almacén cifrado y ejecución real. Ante una desconexión se ofrece actualizar el estado, se mantienen los borradores y se deshabilitan las acciones dependientes hasta confirmar su disponibilidad.

En modo experimental, el acceso del propietario precede a las acciones privadas. La creación exige reconocer que el servicio está aislado y no tiene red ni URL pública; su estado observado y la revisión aceptada son visibles. La parada y la eliminación son acciones separadas; la eliminación requiere confirmación accesible y conserva la definición y la configuración. Ante un resultado incierto se ofrece consultar, sin repetir automáticamente la acción. Las pantallas de simulación permanecen separadas del modo experimental.
