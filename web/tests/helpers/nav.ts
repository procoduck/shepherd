import type { Page } from '@playwright/test';
import type { MeResponse } from '../fixtures/personas';

export async function loginAs(page: Page, persona: MeResponse | null, orgId = 'org-0001') {
  // Navigation helper — sets persona in state before going to a URL.
  // State mutation is done via the api fixture; this helper just navigates.
  await page.goto(`/?org=${orgId}`);
}

export async function goto(page: Page, path: string, orgId = 'org-0001') {
  const url = path.includes('?') ? `${path}&org=${orgId}` : `${path}?org=${orgId}`;
  await page.goto(url);
}
