import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearPendingInvitation, getPendingInvitation, setPendingInvitation } from './pendingInvitation';

describe('pending invitation', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    sessionStorage.clear();
  });

  it('round-trips the token through sessionStorage', () => {
    expect(getPendingInvitation()).toBeNull();
    setPendingInvitation('tok');
    expect(getPendingInvitation()).toBe('tok');
    clearPendingInvitation();
    expect(getPendingInvitation()).toBeNull();
  });

  it('degrades to "no token" when storage is blocked', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    expect(() => {
      setPendingInvitation('tok');
    }).not.toThrow();
    expect(getPendingInvitation()).toBeNull();
  });
});
