import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { isValidDomain } from "./validation";

/** Domain codes the server answers with; they are translated like any other API error. */
export const domainCodes = ["domain_taken", "domain_reserved", "invalid_domain"] as const;
export type DomainCode = (typeof domainCodes)[number];
export const isDomainCode = (code: unknown): code is DomainCode => domainCodes.includes(code as DomainCode);

const reasonCode = { taken: "domain_taken", reserved: "domain_reserved", invalid: "invalid_domain" } as const;

/**
 * Advisory availability of a domain, asked after a pause in typing. The
 * server's unique index decides at save time; a failed check never blocks.
 * Returns the code of a known problem, or undefined.
 */
export function useDomainCheck(domain: string, enabled: boolean, application?: string): DomainCode | undefined {
  const [settled, setSettled] = useState(domain);
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(domain), 400);
    return () => window.clearTimeout(timer);
  }, [domain]);
  const check = useQuery({
    queryKey: ["domain-check", settled, application],
    queryFn: () => api.workerDomainCheck(settled, application),
    enabled: enabled && settled === domain && isValidDomain(settled),
    retry: false,
    staleTime: 2_000,
  });
  if (!enabled || settled !== domain || !check.data || check.data.available) return undefined;
  return check.data.reason ? reasonCode[check.data.reason] : "domain_taken";
}
