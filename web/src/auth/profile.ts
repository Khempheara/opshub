import { useEffect } from 'react';
import { ApiError } from '@/lib/api/fetcher';
import { getMe, updateMe } from '@/lib/api/generated/account/account';
import type { UpdateProfileRequest, User } from '@/lib/api/generated/model';
import i18n from '@/i18n';
import { setDisplayPrefs } from '@/preferences/store';
import { getSession, updateSessionUser, useSession } from './session';

/** If-Match value for a user (the API's ETag is "v<version>"). */
export const ifMatch = (u: Pick<User, 'version'>) => `"v${u.version}"`;

/**
 * Updates the profile with optimistic locking. On a version conflict the latest profile is
 * reloaded and the change is applied once more (safe for last-writer-wins fields like
 * language); the caller sees the conflict only if the retry also fails.
 */
export async function saveProfile(patch: UpdateProfileRequest): Promise<User> {
  const current = getSession().user;
  if (!current) throw new Error('not signed in');
  try {
    const updated = await updateMe(patch, { headers: { 'If-Match': ifMatch(current) } });
    updateSessionUser(updated);
    return updated;
  } catch (err) {
    if (!(err instanceof ApiError) || err.code !== 'VERSION_CONFLICT') throw err;
    const latest = await getMe();
    updateSessionUser(latest);
    const updated = await updateMe(patch, { headers: { 'If-Match': ifMatch(latest) } });
    updateSessionUser(updated);
    return updated;
  }
}

/**
 * Applies the signed-in user's saved preferences (language, time zone, numerals). The
 * profile takes priority over the browser's saved choice and Accept-Language.
 */
export function useProfilePreferences(): void {
  const { user } = useSession();
  useEffect(() => {
    if (!user) return;
    if (i18n.resolvedLanguage !== user.locale) void i18n.changeLanguage(user.locale);
    setDisplayPrefs({ timeZone: user.timezone, khmerNumerals: user.khmer_numerals });
  }, [user]);
}
