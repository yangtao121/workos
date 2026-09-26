// Diagnostic GPU/browser proof. Run inside workos-playwright-shared with /tmp
// mounted as /hosttmp; launch JSON must come from the isolated Greenfield
// fixture, never from a production WorkOS session. The key is never printed.
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const launchPath = process.env.GREENFIELD_LAUNCH_JSON || '/hosttmp/workos-gf-image-launch.json';
const fixturePath = process.env.GREENFIELD_FIXTURE || '/hosttmp/workos-gf-image-fixture/note.txt';
const outputDir = process.env.GREENFIELD_OUTPUT_DIR || '/hosttmp';
const origin = process.env.GREENFIELD_VITE_ORIGIN || 'http://127.0.0.1:5173';
const launch = JSON.parse(fs.readFileSync(launchPath, 'utf8'));
if (fs.readFileSync(fixturePath, 'utf8') !== 'line-one\n') {
  throw new Error('fixture must start with line-one\\n in a fresh Code profile');
}

async function routeLaunch(page) {
  await page.route('http://127.0.0.1:8081/code', route => route.fulfill({
    status: 200,
    headers: {
      'content-type': 'application/json',
      'access-control-allow-origin': origin,
      'access-control-allow-credentials': 'true',
    },
    body: JSON.stringify(launch),
  }));
}

(async () => {
  const browser = await chromium.launch({headless: true, args: ['--no-sandbox', '--enable-webgl', '--ignore-gpu-blocklist']});
  try {
    const page = await browser.newPage({viewport: {width: 1440, height: 900}, deviceScaleFactor: 1});
    await routeLaunch(page);
    await page.goto(origin + '/greenfield-proof.html');
    await page.waitForTimeout(15000);
    if (await page.evaluate(() => document.body.dataset.ready) !== '1') {
      throw new Error('Greenfield proof did not initialize');
    }
    // Code 1.139 may show a first-run Copilot sign-in modal. This coordinate
    // selects its "Continue without Signing In" action; on later runs it is
    // harmless empty canvas. Escape dismisses a remaining first-run overlay.
    await page.mouse.click(1000, 690);
    await page.keyboard.press('Escape');
    await page.waitForTimeout(1500);
    await page.screenshot({path: path.join(outputDir, 'greenfield-code-before.png')});
    await page.mouse.click(250, 164);
    await page.keyboard.press('End');
    await page.keyboard.type('-edited', {delay: 80});
    await page.keyboard.press('Control+s');
    await page.waitForTimeout(1500);
    const saved = fs.readFileSync(fixturePath, 'utf8');
    if (saved !== 'line-one-edited\n') {
      throw new Error('Code GUI save did not update fixture; got ' + JSON.stringify(saved));
    }
    await page.screenshot({path: path.join(outputDir, 'greenfield-code-saved.png')});
    await page.keyboard.type('-unsaved', {delay: 80});
    await page.screenshot({path: path.join(outputDir, 'greenfield-code-unsaved.png')});
    if (fs.readFileSync(fixturePath, 'utf8') !== saved) {
      throw new Error('unsaved text unexpectedly reached disk');
    }
    await page.close();
    const second = await browser.newPage({viewport: {width: 1440, height: 900}, deviceScaleFactor: 1});
    await routeLaunch(second);
    await second.goto(origin + '/greenfield-proof.html');
    await second.waitForTimeout(8000);
    await second.screenshot({path: path.join(outputDir, 'greenfield-code-reconnected.png')});
    console.log(JSON.stringify({codePID: launch.pid, guiSaved: saved.trim(), reconnectCanvasReady: await second.evaluate(() => document.body.dataset.ready === '1')}));
  } finally {
    await browser.close();
  }
})().catch(error => {console.error(error); process.exit(1)});
