import { readFileSync } from 'node:fs';
import satori from 'satori';
import sharp from 'sharp';
import { describe, expect, it } from 'vitest';
import { ContentOgTemplate } from './content.js';
import { HomeOgTemplate } from './home.js';
import { LabelOgTemplate } from './label.js';
import { DEFAULT_CONTENT_OG_CONFIG } from '../types/config.js';

function serialized(value: unknown): string {
  return JSON.stringify(value);
}

describe('OG templates', () => {
  it('renders the default content fallback for a short title without images', () => {
    const template = ContentOgTemplate({
      title: 'Short title',
      settings: { siteTitle: 'Example Studio', primaryColor: '#b02d23' },
    });
    const output = serialized(template);
    const rendered = JSON.parse(output) as {
      props: { style: { backgroundColor?: string } };
    };

    expect(rendered.props.style.backgroundColor).toBe('#1A1B1E');
    expect(output).toContain('Short title');
    expect(output).toContain('Example Studio');
    expect(output).toContain('transparent');
    expect(output).toContain('56px');
  });

  it('renders the configured fallback as an opaque canvas', async () => {
    const svg = await satori(
      ContentOgTemplate({
        title: '',
        settings: { siteTitle: '', primaryColor: '#b02d23' },
        config: { ...DEFAULT_CONTENT_OG_CONFIG, darkBackground: '#123456' },
      }),
      {
        width: 1200,
        height: 630,
        fonts: [
          {
            name: 'Noto Sans',
            weight: 400,
            style: 'normal',
            data: readFileSync('assets/fonts/NotoSans-Regular.ttf'),
          },
        ],
      }
    );
    const { data, info } = await sharp(Buffer.from(svg)).raw().toBuffer({ resolveWithObject: true });
    const pixel = (x: number, y: number) => {
      const offset = (y * info.width + x) * info.channels;
      return Array.from(data.subarray(offset, offset + info.channels));
    };

    expect(pixel(0, 0)).toEqual([18, 52, 86, 255]);
    expect(pixel(600, 315)).toEqual([18, 52, 86, 255]);
    expect(pixel(1199, 629)).toEqual([18, 52, 86, 255]);
  });

  it('truncates long content and renders featured image and logo branches', () => {
    const template = ContentOgTemplate({
      title: '123456789',
      settings: {
        siteTitle: 'Example Studio',
        primaryColor: '#b02d23',
        featuredImageDataUrl: 'data:image/jpeg;base64,image',
        logoSvg: 'data:image/png;base64,logo',
      },
      config: {
        darkBackground: '#000',
        title: {
          maxLength: 8,
          fontSizeThreshold: 3,
          fontSizeLarge: 60,
          fontSizeSmall: 40,
          fontWeight: 700,
          color: '#fff',
          lineHeight: 1.2,
          padding: { top: 1, right: 2, bottom: 3, left: 4 },
        },
        logo: { width: 10, height: 11, position: { bottom: 12, right: 13 } },
        siteTitle: { fontSize: 14, fontWeight: 600, color: '#fff', opacity: 0.8 },
      },
    });
    const output = serialized(template);

    expect(output).toContain('12345...');
    expect(output).toContain('data:image/jpeg;base64,image');
    expect(output).toContain('data:image/png;base64,logo');
    expect(output).toContain('40px');
  });

  it('renders homepage with default site title and with custom image assets', () => {
    const fallback = serialized(
      HomeOgTemplate({ settings: { siteTitle: 'Example Studio', primaryColor: '#b02d23' } })
    );
    const visual = serialized(
      HomeOgTemplate({
        settings: {
          siteTitle: 'Example Studio',
          primaryColor: '#b02d23',
          featuredImageDataUrl: 'data:image/jpeg;base64,image',
          logoSvg: 'data:image/png;base64,logo',
        },
        config: {
          darkBackground: '#111',
          logo: { width: 80, height: 90 },
          siteTitle: { fontSize: 40, fontWeight: 700, color: '#fff' },
        },
      })
    );

    expect(fallback).toContain('Example Studio');
    expect(fallback).not.toContain('rgba(0, 0, 0, 0.36)');
    expect(visual).toContain('data:image/jpeg;base64,image');
    expect(visual).toContain('data:image/png;base64,logo');
    expect(visual).toContain('rgba(0, 0, 0, 0.36)');
  });

  it('renders only the Label logo with contain sizing and no text or Site branding', () => {
    const output = serialized(
      LabelOgTemplate({
        settings: {
          siteTitle: 'Must not render',
          primaryColor: '#b02d23',
          featuredImageDataUrl: 'data:image/png;base64,label-logo',
          logoSvg: 'data:image/png;base64,site-logo',
        },
      })
    );

    expect(output).toContain('data:image/png;base64,label-logo');
    expect(output).toContain('objectFit');
    expect(output).toContain('contain');
    expect(output).toContain('720');
    expect(output).toContain('315');
    expect(output).not.toContain('Must not render');
    expect(output).not.toContain('data:image/png;base64,site-logo');
  });

  it('renders an empty Label canvas when no logo is available', () => {
    const output = serialized(
      LabelOgTemplate({ settings: { siteTitle: 'Must not render', primaryColor: '#b02d23' } })
    );

    expect(output).not.toContain('img');
    expect(output).not.toContain('Must not render');
  });
});
