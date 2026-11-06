import { sendTestNotification } from './notification-test';

describe('sendTestNotification', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('asks the daemon to deliver the style being picked', async () => {
    const fetchMock = vi.fn(async () => ({ ok: true }) as Response);
    vi.stubGlobal('fetch', fetchMock);

    await sendTestNotification('both');

    expect(fetchMock).toHaveBeenCalledWith('/api/notifications/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ style: 'both' }),
    });
  });

  // "None" goes to the daemon like any other style: which styles deliver
  // nothing is the daemon's decision, not a rule duplicated here.
  it('sends the silent style too rather than deciding for the daemon', async () => {
    const fetchMock = vi.fn(async () => ({ ok: true }) as Response);
    vi.stubGlobal('fetch', fetchMock);

    await sendTestNotification('none');

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/notifications/test',
      expect.objectContaining({ body: JSON.stringify({ style: 'none' }) }),
    );
  });

  it('swallows an unreachable daemon — a test is best-effort', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('offline');
      }),
    );

    await expect(sendTestNotification('in-app')).resolves.toBeUndefined();
  });
});
