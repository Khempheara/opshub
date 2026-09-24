// An invitation token survives the sign-in or sign-up detour in sessionStorage: it stays in
// this tab only and is cleared once the invitation is accepted or dismissed. Storage can be
// unavailable (private mode, blocked site data), so every access is guarded.
const KEY = 'opshub.pendingInvitation';

export const ACCEPT_INVITATION_PATH = '/invitations/accept';

export function getPendingInvitation(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}

export function setPendingInvitation(token: string): void {
  try {
    sessionStorage.setItem(KEY, token);
  } catch {
    // Without storage the invitee opens the email link again after signing in.
  }
}

export function clearPendingInvitation(): void {
  try {
    sessionStorage.removeItem(KEY);
  } catch {
    // Nothing to clear.
  }
}
