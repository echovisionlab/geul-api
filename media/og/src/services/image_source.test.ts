import sharp from 'sharp';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  PermanentGenerationError,
  TransientGenerationError,
} from './errors.js';
import { fetchImageAsDataUrl, transformFetchedImage } from './image_source.js';

describe('source image acquisition', () => {
  const transform = vi.fn(async () => ({ buffer: Buffer.from('small'), mimeType: 'image/png' as const }));

  beforeEach(() => transform.mockClear());

  it('fetches and embeds a valid image', async () => {
    const fetcher = vi.fn(async () => new Response(Buffer.from('image'), {
      status: 200,
      headers: { 'content-type': 'image/png', 'content-length': '5' },
    }));
    await expect(fetchImageAsDataUrl('https://cdn/image', 'logo', fetcher, transform)).resolves.toBe('data:image/png;base64,c21hbGw=');
    expect(transform).toHaveBeenCalledWith(Buffer.from('image'), 'logo');
  });

  it.each([
    [429, 'TransientGenerationError'],
    [500, 'TransientGenerationError'],
    [599, 'TransientGenerationError'],
    [404, 'PermanentGenerationError'],
  ])('classifies HTTP %s as %s', async (status, name) => {
    const fetcher = vi.fn(async () => new Response('', { status }));
    await expect(fetchImageAsDataUrl('https://cdn/image', 'featured', fetcher, transform)).rejects.toMatchObject({ name });
  });

  it('classifies fetch and body read failures as transient', async () => {
    const failedFetch = vi.fn(async () => { throw new TypeError('network'); });
    await expect(fetchImageAsDataUrl('https://cdn/image', 'logo', failedFetch, transform)).rejects.toBeInstanceOf(TransientGenerationError);
    const failedBody = vi.fn(async () => ({
      ok: true,
      status: 200,
      headers: new Headers({ 'content-type': 'image/png' }),
      arrayBuffer: async () => { throw new Error('socket reset'); },
    }) as unknown as Response);
    await expect(fetchImageAsDataUrl('https://cdn/image', 'logo', failedBody, transform)).rejects.toBeInstanceOf(TransientGenerationError);
  });

  it('aborts a hung source fetch before it can consume the processing lease', async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn((_url: string | URL | Request, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => {
          reject(Object.assign(new Error('aborted'), { name: 'AbortError' }));
        });
      })
    );
    const operation = fetchImageAsDataUrl('https://cdn/hung', 'logo', fetcher, transform);
    const rejection = expect(operation).rejects.toBeInstanceOf(TransientGenerationError);
    await vi.advanceTimersByTimeAsync(15_000);
    await rejection;
    expect(fetcher.mock.calls[0][1]?.signal?.aborted).toBe(true);
    vi.useRealTimers();
  });

  it('keeps the same timeout active while a response body is streaming', async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn(async (_url: string | URL | Request, init?: RequestInit) => ({
      ok: true,
      status: 200,
      headers: new Headers({ 'content-type': 'image/png' }),
      arrayBuffer: () => new Promise<ArrayBuffer>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new Error('body aborted')));
      }),
    }) as unknown as Response);
    const operation = fetchImageAsDataUrl('https://cdn/slow-body', 'logo', fetcher, transform);
    const rejection = expect(operation).rejects.toBeInstanceOf(TransientGenerationError);
    await vi.advanceTimersByTimeAsync(15_000);
    await rejection;
    expect(fetcher.mock.calls[0][1]?.signal?.aborted).toBe(true);
    vi.useRealTimers();
  });

  it.each([
    ['non-image', new Response('text', { headers: { 'content-type': 'text/plain' } })],
    ['missing type', new Response(new Uint8Array([1]))],
    ['empty', new Response('', { headers: { 'content-type': 'image/png' } })],
    ['oversized header', new Response('x', { headers: { 'content-type': 'image/png', 'content-length': String(9 * 1024 * 1024) } })],
    ['oversized body', new Response(Buffer.alloc(8 * 1024 * 1024 + 1), { headers: { 'content-type': 'image/png' } })],
  ])('rejects deterministic invalid source: %s', async (_name, response) => {
    await expect(fetchImageAsDataUrl('https://cdn/image', 'featured', vi.fn(async () => response), transform)).rejects.toBeInstanceOf(PermanentGenerationError);
  });

  it('rejects an oversized transformed data URL', async () => {
    const response = new Response('x', { headers: { 'content-type': 'image/png' } });
    const huge = vi.fn(async () => ({ buffer: Buffer.alloc(1_200_000), mimeType: 'image/png' as const }));
    await expect(fetchImageAsDataUrl('https://cdn/image', 'featured', vi.fn(async () => response), huge)).rejects.toMatchObject({ errorCode: 'processed_image_too_large' });
  });

  it('accepts a valid high-entropy Label logo after the 720x315 PNG transform', async () => {
    const pixels = Buffer.alloc(720 * 315 * 4);
    let state = 0x12345678;
    for (let index = 0; index < pixels.length; index += 1) {
      state ^= state << 13;
      state ^= state >>> 17;
      state ^= state << 5;
      pixels[index] = state & 0xff;
    }
    const transformed = await sharp(pixels, {
      raw: { width: 720, height: 315, channels: 4 },
    }).png().toBuffer();
    expect(transformed.length).toBeGreaterThan(500_000);

    const response = new Response('x', { headers: { 'content-type': 'image/png' } });
    const transform = vi.fn(async () => ({ buffer: transformed, mimeType: 'image/png' as const }));
    await expect(
      fetchImageAsDataUrl(
        'https://cdn/label-logo.png',
        'label-logo',
        vi.fn(async () => response),
        transform
      )
    ).resolves.toMatch(/^data:image\/png;base64,/);
  });

  it('transforms featured and logo inputs to their bounded raster formats', async () => {
    const source = await sharp({ create: { width: 2, height: 2, channels: 4, background: '#ff0000' } }).png().toBuffer();
    const featured = await transformFetchedImage(source, 'featured');
    const logo = await transformFetchedImage(source, 'logo');
    const labelLogo = await transformFetchedImage(source, 'label-logo');
    expect(featured.mimeType).toBe('image/jpeg');
    expect(logo.mimeType).toBe('image/png');
    expect(labelLogo.mimeType).toBe('image/png');
    expect((await sharp(featured.buffer).metadata()).width).toBe(1200);
    expect((await sharp(logo.buffer).metadata()).width).toBe(2);
    expect((await sharp(labelLogo.buffer).metadata()).width).toBe(2);
  });
});
