import { useTranslation } from 'react-i18next';
import { Badge } from '@/components/ui/badge';
import type { Role } from '@/lib/api/generated/model';
import { cn } from '@/lib/utils';
import { selectClass } from './constants';

/** Role badge; role names stay English in both languages (see the glossary). */
export function RoleBadge({ role }: { role: Role }) {
  const { t } = useTranslation();
  return (
    <Badge variant={role === 'owner' || role === 'admin' ? 'default' : 'secondary'} title={role}>
      {t(`roles.${role}`)}
    </Badge>
  );
}

/** A native select of roles with an accessible name. */
export function RoleSelect({
  value,
  roles,
  label,
  onChange,
  disabled,
  className,
}: {
  value: Role;
  roles: readonly Role[];
  label: string;
  onChange: (role: Role) => void;
  disabled?: boolean;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    <select
      aria-label={label}
      className={cn(selectClass, className)}
      value={value}
      disabled={disabled}
      onChange={(e) => {
        onChange(e.target.value as Role);
      }}
    >
      {roles.map((r) => (
        <option key={r} value={r}>
          {t(`roles.${r}`)}
        </option>
      ))}
    </select>
  );
}
