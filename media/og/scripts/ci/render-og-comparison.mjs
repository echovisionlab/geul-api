import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import satori from 'satori';
import sharp from 'sharp';
import { ContentOgTemplate } from '../../src/templates/content.tsx';
import { HomeOgTemplate } from '../../src/templates/home.tsx';
import { LabelOgTemplate } from '../../src/templates/label.tsx';

const [baselineDirectory, outputDirectory] = process.argv.slice(2);
if (!baselineDirectory || !outputDirectory) {
  throw new Error('Expected baseline package directory and output directory');
}
const baselineRequire = createRequire(path.join(path.resolve(baselineDirectory), 'package.json'));
const baselineModule = await import(pathToFileURL(baselineRequire.resolve('satori')).href);
const baselineSatori = typeof baselineModule.default === 'function'
  ? baselineModule.default
  : baselineModule.default.default;
if (typeof baselineSatori !== 'function') {
  throw new Error('Baseline Satori does not export a renderer');
}
await mkdir(outputDirectory, { recursive: true });
const fonts = await Promise.all([
  ['Noto Sans', 'NotoSans-Regular.ttf', 400],
  ['Noto Sans', 'NotoSans-Bold.ttf', 700],
  ['Noto Sans KR', 'NotoSansKR-Regular.ttf', 400],
  ['Noto Sans KR', 'NotoSansKR-Bold.ttf', 700],
].map(async ([name, file, weight]) => ({ name, weight, style: 'normal', data: await readFile(`assets/fonts/${file}`) })));

// In-memory assets keep the rendering review independent of network services.
const feature = 'data:image/png;base64,' + (await sharp({
  create: { width: 1200, height: 630, channels: 4, background: '#6b3867' },
}).png().toBuffer()).toString('base64');
const logo = 'data:image/png;base64,' + (await sharp({
  create: { width: 480, height: 240, channels: 4, background: '#dbb750' },
}).png().toBuffer()).toString('base64');
const settings = { siteTitle: 'Example Studio', primaryColor: '#b02d23' };
const fixtures = [
  ['content-fallback', ContentOgTemplate({ title: 'A listening room for independent music', settings })],
  ['content-featured', ContentOgTemplate({
    title: 'Independent music, artists and stories from the listening room',
    settings: { ...settings, featuredImageDataUrl: feature, logoSvg: logo },
  })],
  ['content-korean', ContentOgTemplate({ title: '독립 음악과 아티스트의 새로운 이야기', settings })],
  ['home-fallback', HomeOgTemplate({ settings })],
  ['home-featured', HomeOgTemplate({ settings: { ...settings, featuredImageDataUrl: feature, logoSvg: logo } })],
  ['label-logo', LabelOgTemplate({ settings: { ...settings, featuredImageDataUrl: logo } })],
];
const dimensions = { width: 1200, height: 630, fonts };
const summary = [];
for (const [name, template] of fixtures) {
  const renders = [];
  for (const [version, render] of [['before', baselineSatori], ['after', satori]]) {
    const svg = await render(template, dimensions);
    const png = await sharp(Buffer.from(svg)).png().toBuffer();
    await writeFile(path.join(outputDirectory, `${name}-${version}.png`), png);
    renders.push(png);
  }
  const comparison = await sharp({
    create: { width: 2400, height: 630, channels: 4, background: '#fff' },
  }).composite(renders.map((input, index) => ({ input, left: index * 1200, top: 0 }))).png().toBuffer();
  await writeFile(path.join(outputDirectory, `${name}-comparison.png`), comparison);
  const before = await sharp(renders[0]).raw().toBuffer();
  const after = await sharp(renders[1]).raw().toBuffer();
  let changedChannels = 0;
  for (let index = 0; index < before.length; index++) {
    if (before[index] !== after[index]) changedChannels++;
  }
  summary.push({ fixture: name, changedChannels, totalChannels: before.length });
}
await writeFile(path.join(outputDirectory, 'summary.json'), JSON.stringify(summary, null, 2));
console.log('Rendered six OG fixtures. Comparison PNGs show the base version on the left and the updated version on the right.');
