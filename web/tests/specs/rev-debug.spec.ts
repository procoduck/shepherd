import { basicScenario } from '../fixtures/factories';
import { appAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

test('debug load failure', async ({ page, api }) => {
  await api.loginAs(appAdmin);
  const s = basicScenario();
  api.seed({ orgs: [s.org] });
  api.failNext('POST', '/shepherd.mgmt.v1.PipelineService/ListPipelines', 503, 'unavailable');

  const reqs: string[] = [];
  const t0 = Date.now();
  page.on('request', (r) => {
    const u = r.url().replace('http://localhost:4173', '');
    if (u.startsWith('/api')) reqs.push(r.method() + ' ' + u + '@' + (Date.now() - t0));
  });
  page.on('response', (r) => {
    const u = r.url().replace('http://localhost:4173', '');
    if (u.includes('pipelines'))
      console.log('PIPELINE RESP:', r.status(), u + '@' + (Date.now() - t0));
  });

  await page.goto('/pipelines');
  await page.waitForTimeout(2000);

  const alertCount = await page.getByRole('alert').count();
  const errorText = await page.getByText(/error|failed|retry|unavailable/i).count();
  const mainText = await page.locator('main').textContent();

  console.log('alertCount:', alertCount, 'errorTextCount:', errorText);
  console.log('main:', mainText?.slice(0, 300));
  console.log('reqs:', reqs.join(' | '));
});
