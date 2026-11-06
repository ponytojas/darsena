import { useRef, type KeyboardEvent } from "react";
import { Check } from "lucide-react";
import { useI18n } from "@/i18n/context";
import { accents, type Accent } from "@/lib/theme";

/** The twelve palette swatches as a radio group: one tab stop, arrows move and select. */
export function ColorPicker({
  value,
  onChange,
  label,
}: {
  value: Accent;
  onChange: (value: Accent) => void;
  label: string;
}) {
  const { t } = useI18n();
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const select = (index: number) => {
    const next = (index + accents.length) % accents.length;
    onChange(accents[next]);
    refs.current[next]?.focus();
  };
  const onKeyDown = (event: KeyboardEvent, index: number) => {
    const step =
      event.key === "ArrowRight" || event.key === "ArrowDown" ? 1
      : event.key === "ArrowLeft" || event.key === "ArrowUp" ? -1
      : 0;
    if (event.metaKey || event.ctrlKey || event.altKey) return;
    if (step) select(index + step);
    else if (event.key === "Home") select(0);
    else if (event.key === "End") select(accents.length - 1);
    else return;
    event.preventDefault();
  };
  return (
    <div role="radiogroup" aria-label={label} className="accent-options">
      {accents.map((color, index) => (
        <button
          key={color}
          ref={(node) => {
            refs.current[index] = node;
          }}
          type="button"
          role="radio"
          aria-checked={value === color}
          aria-label={t.accentLabels[color]}
          title={t.accentLabels[color]}
          tabIndex={value === color ? 0 : -1}
          data-color={color}
          className="accent-option"
          onClick={() => onChange(color)}
          onKeyDown={(event) => onKeyDown(event, index)}
        >
          {value === color && <Check aria-hidden="true" />}
        </button>
      ))}
    </div>
  );
}
