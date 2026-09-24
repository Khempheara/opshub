import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import i18n from 'i18next';
import { afterEach, describe, expect, it } from 'vitest';
import { LOCALE_STORAGE_KEY } from '@/i18n';
import { LanguageSwitcher } from './LanguageSwitcher';

afterEach(async () => {
  await i18n.changeLanguage('en');
});

describe('LanguageSwitcher', () => {
  it('switches language, updates <html lang> and persists the choice', async () => {
    await i18n.changeLanguage('en');
    render(<LanguageSwitcher />);

    const group = screen.getByRole('group', { name: 'Language' });
    expect(screen.getByRole('button', { name: 'EN' })).toHaveAttribute('aria-pressed', 'true');

    await userEvent.click(screen.getByRole('button', { name: 'ខ្មែរ' }));

    expect(i18n.resolvedLanguage).toBe('km');
    expect(document.documentElement.lang).toBe('km');
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('km');
    expect(group).toHaveAccessibleName('ភាសា');
    expect(screen.getByRole('button', { name: 'ខ្មែរ' })).toHaveAttribute('aria-pressed', 'true');
  });
});
