import { Input } from "@/components/ui/input";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import type { Access } from "@/api/client";
import { useI18n } from "@/i18n/context";

const selectClass =
  "h-8 w-full rounded-lg border border-input bg-transparent px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30";
const levels: Access[] = ["isolated", "internal", "local"];

/** Where an application listens on this computer; the host port is never part of it. */
const localHost = (domain: string) => `${domain}.localhost`;

/**
 * Access, common network and domain, shared by creation and settings. The
 * notes are part of the control: network access is a real trade-off.
 */
export function AccessFields({
  ids,
  access,
  onAccess,
  shared,
  onShared,
  domain,
  onDomain,
  domainError,
  appsPort,
  suggestedPort,
  disabled,
  lockedNote,
}: {
  ids: string;
  access: Access;
  onAccess: (value: Access) => void;
  shared: boolean;
  onShared: (value: boolean) => void;
  domain: string;
  onDomain: (value: string) => void;
  domainError: string;
  appsPort?: number;
  /** Set while the access shown was preselected from the image's EXPOSE. */
  suggestedPort?: number;
  disabled?: boolean;
  lockedNote?: string;
}) {
  const { t } = useI18n();
  const a = t.definitions.access;
  return (
    <>
      <Field>
        <FieldLabel htmlFor={`${ids}-access`}>{a.label}</FieldLabel>
        <select
          id={`${ids}-access`}
          className={selectClass}
          value={access}
          disabled={disabled}
          aria-describedby={`${ids}-access-help`}
          onChange={(event) => onAccess(event.target.value as Access)}
        >
          {levels.map((level) => (
            <option key={level} value={level}>
              {a.levels[level]}
            </option>
          ))}
        </select>
        <FieldDescription id={`${ids}-access-help`}>
          {suggestedPort !== undefined && <strong className="font-medium text-foreground">{a.suggested(suggestedPort)} </strong>}
          {a.hint[access]}
        </FieldDescription>
        {lockedNote && <FieldDescription>{lockedNote}</FieldDescription>}
      </Field>
      {access !== "isolated" && (
        <>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" checked={shared} disabled={disabled} onChange={(event) => onShared(event.target.checked)} />
            {a.shared}
          </label>
          {access === "local" && (
            <Field data-invalid={!!domainError}>
              <FieldLabel htmlFor={`${ids}-domain`}>{a.domain}</FieldLabel>
              <Input
                id={`${ids}-domain`}
                value={domain}
                disabled={disabled}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                aria-invalid={!!domainError}
                aria-describedby={`${ids}-domain-help`}
                className="font-mono"
                onChange={(event) => onDomain(event.target.value)}
              />
              <FieldDescription id={`${ids}-domain-help`}>
                {domain && !domainError
                  ? appsPort
                    ? a.preview(`http://${localHost(domain)}:${appsPort}`)
                    : a.previewNoListener(localHost(domain))
                  : a.domainHint}
              </FieldDescription>
              {domainError && <FieldError>{domainError}</FieldError>}
            </Field>
          )}
          <p className="text-sm text-muted-foreground">{a.risk[access]}</p>
        </>
      )}
    </>
  );
}
