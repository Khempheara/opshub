import { Eye, EyeOff } from 'lucide-react';
import { forwardRef, useId, useState, type ComponentProps, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { cn } from '@/lib/utils';

interface FieldProps {
  label: string;
  error?: string;
  hint?: ReactNode;
  children: (ids: { id: string; describedBy: string | undefined; invalid: boolean }) => ReactNode;
  className?: string;
}

/** Label + control + hint + error, wired for screen readers (aria-describedby/invalid). */
export function Field({ label, error, hint, children, className }: FieldProps) {
  const id = useId();
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(' ') || undefined;
  return (
    <div className={cn('space-y-1.5', className)}>
      <Label htmlFor={id}>{label}</Label>
      {children({ id, describedBy, invalid: Boolean(error) })}
      {hint && (
        <p id={hintId} className="text-muted-foreground text-xs">
          {hint}
        </p>
      )}
      {error && (
        <p id={errorId} role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
    </div>
  );
}

type TextFieldProps = ComponentProps<typeof Input> & { label: string; error?: string; hint?: ReactNode };

/** Text input with label and error; forwards the ref for react-hook-form's register(). */
export const TextField = forwardRef<HTMLInputElement, TextFieldProps>(function TextField(
  { label, error, hint, className, ...props },
  ref,
) {
  return (
    <Field label={label} error={error} hint={hint} className={className}>
      {({ id, describedBy, invalid }) => (
        <Input ref={ref} id={id} aria-describedby={describedBy} aria-invalid={invalid || undefined} {...props} />
      )}
    </Field>
  );
});

/** Password input with a show/hide toggle. */
export const PasswordField = forwardRef<HTMLInputElement, Omit<TextFieldProps, 'type'>>(function PasswordField(
  { label, error, hint, className, ...props },
  ref,
) {
  const { t } = useTranslation();
  const [visible, setVisible] = useState(false);
  return (
    <Field label={label} error={error} hint={hint} className={className}>
      {({ id, describedBy, invalid }) => (
        <div className="relative">
          <Input
            ref={ref}
            id={id}
            type={visible ? 'text' : 'password'}
            aria-describedby={describedBy}
            aria-invalid={invalid || undefined}
            className="pe-10"
            {...props}
          />
          <button
            type="button"
            onClick={() => {
              setVisible((v) => !v);
            }}
            aria-label={visible ? t('actions.hidePassword') : t('actions.showPassword')}
            aria-pressed={visible}
            className="text-muted-foreground hover:text-foreground absolute inset-y-0 end-0 flex w-10 items-center justify-center rounded-e-md"
          >
            {visible ? <EyeOff aria-hidden className="size-4" /> : <Eye aria-hidden className="size-4" />}
          </button>
        </div>
      )}
    </Field>
  );
});
