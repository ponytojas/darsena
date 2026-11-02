import { NavLink } from "react-router";

const tabClass = ({ isActive }: { isActive: boolean }) =>
  `-mb-px border-b-2 px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring ${isActive ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground"}`;

/** Tab strip shared by application detail pages; the first tab matches its path exactly. */
export function DetailTabs({
  label,
  tabs,
}: {
  label: string;
  tabs: { to: string; label: string }[];
}) {
  return (
    <nav aria-label={label} className="mt-6 flex gap-1 border-b border-border">
      {tabs.map((tab, index) => (
        <NavLink key={tab.to} to={tab.to} end={index === 0} className={tabClass}>
          {tab.label}
        </NavLink>
      ))}
    </nav>
  );
}
