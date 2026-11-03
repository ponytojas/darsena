import { expect, type Page } from "@playwright/test";

const SPANISH = /\b(Aplicaciones|Aplicación|Despliegues?|Siguiente|Configuración|Guardar|Actividad|Diagnóstico|Cancelar|Continuar|Resumen|Volver|Buscar|Ajustes|Cerrar|Proyectos?|Historial|Restaurar|Eliminar|Deshacer|Seleccionar|Revisión|Mover|Imagen|Puerto|Sin proyecto)\b/;

/** Visible text of the landmarks, the open dialog and the header; server log lines stay Spanish by design. */
async function chrome(page: Page) {
  return page.evaluate(() => {
    const nodes = document.querySelectorAll("main, nav, header, [role=dialog], [role=alert], [data-sonner-toast]");
    return [...nodes]
      .map((node) => {
        const clone = node.cloneNode(true) as HTMLElement;
        clone.querySelectorAll(".log-section, .log-view").forEach((n) => n.remove());
        return clone.innerText ?? clone.textContent ?? "";
      })
      .join("\n");
  });
}

export async function expectEnglish(page: Page) {
  await expect.poll(async () => SPANISH.exec(await chrome(page))?.[0] ?? null).toBeNull();
}

