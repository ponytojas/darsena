# Dársena

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Dársena está orientada a un único propietario que gestiona aplicaciones.

## Product Purpose

Dársena es un PaaS de código abierto. Su interfaz actual, en español e inglés (selector en Ajustes), ofrece un espacio local experimental para explorar flujos de aplicaciones.

## Operating Context

Los flujos principales parten de una única lista de aplicaciones (agrupada, con búsqueda y filtros) y de la paleta ⌘K: abrir una aplicación y seguir su «siguiente paso», simular un despliegue de un ejemplo, consultar la actividad, crear una aplicación eligiendo antes el tipo (ejemplo simulado o definición guardada) y gestionar definiciones: variables, historial de configuración con restauración de un valor anterior, ajustes (proyecto, imagen, puerto) y eliminación. Las definiciones se agrupan en proyectos (creación, edición de nombre y color, eliminación con «Deshacer» y movimiento individual o en bloque); eliminar un proyecto nunca borra aplicaciones. Una página de Diagnóstico explica el estado del servidor, el worker y la ejecución real, con el comando o documento que resuelve cada carencia. La interfaz consulta las capacidades del servidor para distinguir un worker no configurado de uno no disponible, informar del almacén cifrado y mostrar si la ejecución real experimental está habilitada y disponible. Cuando no puede confirmar el estado, conserva los borradores y bloquea las acciones que dependen del worker o del almacén.

## Capabilities and Constraints

Las aplicaciones y despliegues de ejemplo viven en memoria y se reinician con el servidor; sus URLs y resultados son ficticios. En demo, las aplicaciones guardadas conservan definiciones y variables sin desplegar servicios. El modo experimental local y autenticado permite una creación inicial por aplicación desde una imagen por digest (una etiqueta pública se resuelve a su digest, y la imagen se puede descargar explícitamente al runtime), con o sin variables, en un solo «Desplegar» que descarga la imagen si hace falta y puede crear el proyecto en el momento; por defecto la aplicación queda aislada: no publica puertos ni proporciona URL pública. Con acceso «Red interna» tiene red con salida a Internet y la alcanzan por nombre las aplicaciones de su red, sin publicar nada. Con «Abierta en este equipo» tiene además una dirección propia, `http://<dominio>.localhost`, solo en este equipo y sin autenticación: cualquier programa o web abierta en el navegador puede enviarle peticiones. Puede conectarse también a una red común entre proyectos. Cada dominio es único entre aplicaciones: si ya existe, la creación se rechaza y nunca se cambia en silencio. Una aplicación con servicio usado no puede cambiar de acceso ni de dominio. La interfaz permite parar el servicio y eliminar el contenedor detenido con confirmación, conservando la definición y la configuración; no permite recrearlo ni aplicar rotaciones automáticamente. Se ha probado un recorrido de escritorio con Chromium y daemon real en OrbStack; la revisión de seguridad y la validación real en Safari, en móvil y tras reiniciar con servicios abiertos siguen pendientes. Una definición cuyo servicio se ha creado alguna vez no puede cambiar de imagen, porque la recreación del servicio aún no existe; sí puede eliminarse: si su contenedor sigue existiendo, se para y se elimina antes; en una aplicación aislada el puerto es metadato y sigue editable; en las demás queda bloqueado junto con el acceso. Los trabajos del worker son simulaciones persistentes y no modifican infraestructura.
