import { readFileSync } from 'fs';
import { dirname, join } from 'path';
import { fileURLToPath } from 'url';
import satori, { type Font } from 'satori';
import sharp from 'sharp';
import { logger } from '../logger.js';
import { ContentOgTemplate } from '../templates/content.js';
import { HomeOgTemplate } from '../templates/home.js';
import { LabelOgTemplate } from '../templates/label.js';
import type {
  ContentOgImageConfig,
  HomeOgImageConfig,
  LabelOgImageConfig,
  OgImageSettings,
} from '../types/config.js';
import {
  DEFAULT_CONTENT_OG_CONFIG,
  DEFAULT_HOME_OG_CONFIG,
  DEFAULT_LABEL_OG_CONFIG,
} from '../types/config.js';

const __dirname = dirname(fileURLToPath(import.meta.url));

const OG_WIDTH = 1200;
const OG_HEIGHT = 630;
const MAX_RENDERED_SVG_BYTES = 5 * 1024 * 1024; // 5 MiB
const MAX_FEATURED_DATA_URL_BYTES = 1_500_000; // ~1.5 MiB
const MAX_LOGO_DATA_URL_BYTES = 500_000; // ~0.5 MiB

// Font cache to avoid re-reading files
const fontCache = new Map<string, Buffer>();

interface FontConfig {
  name: string;
  weight: 400 | 700;
  filename: string;
  system?: boolean;
}

// Local font files in assets/fonts/
const FONT_CONFIGS: FontConfig[] = [
  { name: 'Noto Sans', weight: 400, filename: 'NotoSans-Regular.ttf' },
  { name: 'Noto Sans', weight: 700, filename: 'NotoSans-Bold.ttf' },
  { name: 'Noto Sans KR', weight: 400, filename: 'NotoSansKR-Regular.ttf' },
  { name: 'Noto Sans KR', weight: 700, filename: 'NotoSansKR-Bold.ttf' },
  { name: 'Noto Sans JP', weight: 400, filename: 'NotoSansJP-Regular.ttf' },
  { name: 'Noto Sans JP', weight: 700, filename: 'NotoSansJP-Bold.ttf' },
  { name: 'Noto Sans SC', weight: 400, filename: 'NotoSansSC-Regular.ttf' },
  { name: 'Noto Sans SC', weight: 700, filename: 'NotoSansSC-Bold.ttf' },
  { name: 'Noto Sans Arabic', weight: 400, filename: 'NotoSansArabic-Regular.ttf', system: true },
  { name: 'Noto Sans Arabic', weight: 700, filename: 'NotoSansArabic-Bold.ttf', system: true },
  { name: 'Noto Sans Thai', weight: 400, filename: 'NotoSansThai-Regular.ttf', system: true },
  { name: 'Noto Sans Thai', weight: 700, filename: 'NotoSansThai-Bold.ttf', system: true },
];

// Resolve fonts directory - after bundling, __dirname is /app/dist
// so ../assets/fonts resolves to /app/assets/fonts
const FONTS_DIR = join(__dirname, '../assets/fonts');

function loadFont(config: FontConfig): Buffer {
  const cacheKey = `${config.name}-${config.weight}`;
  const cached = fontCache.get(cacheKey);
  if (cached) {
    return cached;
  }

  const fontPath = config.system
    ? join('/usr/share/fonts/noto', config.filename)
    : join(FONTS_DIR, config.filename);
  const buffer = readFileSync(fontPath);
  fontCache.set(cacheKey, buffer);
  return buffer;
}

function loadFonts(): Font[] {
  const fonts: Font[] = [];

  for (const config of FONT_CONFIGS) {
    try {
      const data = loadFont(config);
      fonts.push({
        name: config.name,
        weight: config.weight,
        style: 'normal',
        data,
      });
    } catch (err) {
      logger.warn(
        {
          font: config.filename,
          err,
        },
        'Failed to load font'
      );
    }
  }

  return fonts;
}

function sanitizeDataUrl(
  dataUrl: string | undefined,
  maxBytes: number,
  field: 'featuredImageDataUrl' | 'logoSvg'
): string | undefined {
  if (!dataUrl) {
    return undefined;
  }

  const size = Buffer.byteLength(dataUrl, 'utf8');
  if (size <= maxBytes) {
    return dataUrl;
  }

  logger.warn(
    {
      reason: 'oversized',
      field,
      size,
      maxBytes,
    },
    'Skipping oversized embedded image in OG template'
  );
  return undefined;
}

function sanitizeSettings(settings: OgImageSettings): OgImageSettings {
  return {
    ...settings,
    featuredImageDataUrl: sanitizeDataUrl(
      settings.featuredImageDataUrl,
      MAX_FEATURED_DATA_URL_BYTES,
      'featuredImageDataUrl'
    ),
    logoSvg: sanitizeDataUrl(settings.logoSvg, MAX_LOGO_DATA_URL_BYTES, 'logoSvg'),
  };
}

function assertSvgSize(svg: string): void {
  const size = Buffer.byteLength(svg, 'utf8');
  if (size > MAX_RENDERED_SVG_BYTES) {
    throw new Error(
      `Generated SVG exceeds safe size limit: ${size} bytes (max ${MAX_RENDERED_SVG_BYTES})`
    );
  }
}

type OgTemplate = Parameters<typeof satori>[0];

async function renderOgTemplate(template: OgTemplate): Promise<Buffer> {
  const fonts = loadFonts();
  if (fonts.length === 0) {
    throw new Error('No fonts loaded for OG image generation');
  }

  const svg = await satori(template, {
    width: OG_WIDTH,
    height: OG_HEIGHT,
    fonts,
  });
  assertSvgSize(svg);
  return sharp(Buffer.from(svg)).webp({ quality: 85 }).toBuffer();
}

/**
 * Generate a content OG image as WebP buffer (for post/page/work)
 */
export async function generateContentOgImage(
  title: string,
  settings: OgImageSettings,
  config: ContentOgImageConfig = DEFAULT_CONTENT_OG_CONFIG
): Promise<Buffer> {
  const safeSettings = sanitizeSettings(settings);
  return renderOgTemplate(ContentOgTemplate({ title, settings: safeSettings, config }));
}

/**
 * Generate a homepage OG image as WebP buffer (logo-centered, no title)
 */
export async function generateHomeOgImage(
  settings: OgImageSettings,
  config: HomeOgImageConfig = DEFAULT_HOME_OG_CONFIG
): Promise<Buffer> {
  const safeSettings = sanitizeSettings(settings);
  return renderOgTemplate(HomeOgTemplate({ settings: safeSettings, config }));
}

/** Generate a Label OG image with only its effective logo centered. */
export async function generateLabelOgImage(
  settings: OgImageSettings,
  config: LabelOgImageConfig = DEFAULT_LABEL_OG_CONFIG
): Promise<Buffer> {
  const safeSettings = sanitizeSettings(settings);
  if (!safeSettings.featuredImageDataUrl) {
    throw new Error('Label OG generation requires an effective logo');
  }
  return renderOgTemplate(LabelOgTemplate({ settings: safeSettings, config }));
}
