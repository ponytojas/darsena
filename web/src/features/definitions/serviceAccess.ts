import type { Access, ExperimentalService } from "@/api/client";

/**
 * How the service is reached. The service response says what is deployed and
 * the application what is saved; when both exist, the deployed one counts.
 * The URL is composed here from the domain and the proxy port: the server
 * never exposes the published host port, so a link can't bypass the proxy
 * (127.0.0.1:<port> would send the owner cookie to the application).
 */
export function serviceAccess(
  app: { name: string; port: number; access: Access; domain: string | null },
  service?: Pick<ExperimentalService, "access" | "domain">,
  appsPort?: number,
) {
  const access = service?.access ?? app.access;
  const domain = (service ? service.domain : app.domain) ?? null;
  return {
    access,
    domain,
    host: access === "local" && domain ? `${domain}.localhost` : null,
    internalUrl: `http://${app.name}:${app.port}`,
    openUrl: access === "local" && domain && appsPort ? `http://${domain}.localhost:${appsPort}` : null,
  };
}
