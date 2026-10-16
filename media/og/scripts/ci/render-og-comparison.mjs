import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import satori from 'satori';
import sharp from 'sharp';
import { ContentOgTemplate } from '../../src/templates/content.tsx';
import { HomeOgTemplate } from '../../src/templates/home.tsx';
import { LabelOgTemplate } from '../../src/templates/label.tsx';

const [baselineDirectory, baselineSourceDirectory, outputDirectory] = process.argv.slice(2);
if (!baselineDirectory || !baselineSourceDirectory || !outputDirectory) {
  throw new Error('Expected baseline package, source, and output directories');
}
const baselineRequire = createRequire(path.join(path.resolve(baselineDirectory), 'package.json'));
const baselineModule = await import(pathToFileURL(baselineRequire.resolve('satori')).href);
const baselineSatori = typeof baselineModule.default === 'function'
  ? baselineModule.default
  : baselineModule.default.default;
if (typeof baselineSatori !== 'function') {
  throw new Error('Baseline Satori does not export a renderer');
}
const baselineTemplates = await Promise.all(['content', 'home', 'label'].map(name =>
  import(pathToFileURL(path.resolve(baselineSourceDirectory, `media/og/src/templates/${name}.tsx`)).href)
));
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
const fixtures = (Content, Home, Label) => [
  ['content-fallback', Content({ title: 'A listening room for independent music', settings })],
  ['content-featured', Content({
    title: 'Independent music, artists and stories from the listening room',
    settings: { ...settings, featuredImageDataUrl: feature, logoSvg: logo },
  })],
  ['content-korean', Content({ title: '독립 음악과 아티스트의 새로운 이야기', settings })],
  ['home-fallback', Home({ settings })],
  ['home-featured', Home({ settings: { ...settings, featuredImageDataUrl: feature, logoSvg: logo } })],
  ['label-logo', Label({ settings: { ...settings, featuredImageDataUrl: logo } })],
];
const baselineFixtures = fixtures(baselineTemplates[0].ContentOgTemplate, baselineTemplates[1].HomeOgTemplate, baselineTemplates[2].LabelOgTemplate);
const currentFixtures = fixtures(ContentOgTemplate, HomeOgTemplate, LabelOgTemplate);
const dimensions = { width: 1200, height: 630, fonts };
const summary = [];
for (let fixtureIndex = 0; fixtureIndex < currentFixtures.length; fixtureIndex++) {
  const [name, currentTemplate] = currentFixtures[fixtureIndex];
  const [, baselineTemplate] = baselineFixtures[fixtureIndex];
  const renders = [];
  for (const [version, render, template] of [['before', baselineSatori, baselineTemplate], ['after', satori, currentTemplate]]) {
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
