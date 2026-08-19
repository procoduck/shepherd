/**
 * Fullstack: auth-journey (V4-16a)
 * The full login/logout lifecycle as one ordered journey.
 */
import { expect, test } from './fixtures';

test.describe('auth-journey', () => {
  test('full login/logout lifecycle', async ({ page, context }) => {
    const responses: Array<{ url: string; status: number }> = [];
    const collectResponse = (response: import('@playwright/test').Response) => {
      responses.push({ url: response.url(), status: response.status() });
    };
    page.on('response', collectResponse);

    await page.goto('/');
    await expect(page).toHaveURL(/\/login/);
    await expect(page.getByTestId('local-login-submit')).toBeVisible();

    // The SPA probes identity through the generated Connect client
    // (MeService/GetMe), not the legacy /api/me REST shim which only external
    // integrations use — match either so this asserts the app's real behavior.
    const isMeProbe = (url: string) => url.endsWith('/api/me') || url.includes('MeService/GetMe');
    await expect
      .poll(() => responses.filter((response) => isMeProbe(response.url)).length, {
        message: 'the SPA must probe its identity endpoint while unauthenticated',
      })
      .toBeGreaterThan(0);
    const meResponses = responses.filter((response) => isMeProbe(response.url));
    expect(meResponses.every((response) => response.status === 401)).toBe(true);

    await page.getByTestId('local-username').fill('admin');
    await page.getByTestId('local-password').fill('admin');
    await page.getByTestId('local-login-submit').click();
    await expect(page).toHaveURL('/');

    const cookies = await context.cookies();
    const sessionCookie = cookies.find((cookie) => cookie.name === 'shepherd_session');
    expect(sessionCookie).toBeDefined();
    expect(sessionCookie?.httpOnly).toBe(true);
    expect(sessionCookie?.sameSite).toBe('Lax');
    expect(sessionCookie?.path).toBe('/');

    await page.goto('/pipelines');
    await expect(page).toHaveURL('/pipelines');

    const preLogoutCookieValue = sessionCookie?.value;
    await page.getByText('Sign out', { exact: true }).click();
    await expect(page).toHaveURL(/\/login/);

    const cookiesAfterLogout = await context.cookies();
    expect(cookiesAfterLogout.find((cookie) => cookie.name === 'shepherd_session')).toBeUndefined();

    const replayResp = await page.request.get('/api/me', {
      headers: {
        Cookie: `shepherd_session=${preLogoutCookieValue}`,
        'X-Requested-With': 'XMLHttpRequest',
      },
    });
    expect(replayResp.status()).toBe(401);

    await page.goBack();
    await expect(page).toHaveURL(/\/login/);

    await context.clearCookies();
    await page.goto('/pipelines');
    await expect(page).toHaveURL(/\/login/);
  });
});
