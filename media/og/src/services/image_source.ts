import sharp from 'sharp';
import { PermanentGenerationError, TransientGenerationError } from './errors.js';
import { withAbortTimeout } from './timeout.js';

const FEATURED_IMAGE_WIDTH = 1200;
const FEATURED_IMAGE_HEIGHT = 630;
const LOGO_MAX_SIZE = 400;
const LABEL_LOGO_MAX_WIDTH = 720;
const LABEL_LOGO_MAX_HEIGHT = 315;
const MAX_FETCHED_IMAGE_BYTES = 8 * 1024 * 1024;
const SOURCE_FETCH_TIMEOUT_MS = 15_000;

export type ImageUsage = 'featured' | 'logo' | 'label-logo';

const MAX_DATA_URL_BYTES: Record<ImageUsage, number> = {
  featured: 1_500_000,
  logo: 500_000,
  'label-logo': 1_500_000,
};

interface TransformedImage {
  buffer: Buffer;
  mimeType: 'image/jpeg' | 'image/png';
}

export async function transformFetchedImage(
  buffer: Buffer,
  usage: ImageUsage
): Promise<TransformedImage> {
  const image = sharp(buffer, { limitInputPixels: 40_000_000 }).rotate();
  if (usage === 'featured') {
    const transformed = await image
      .resize({
        width: FEATURED_IMAGE_WIDTH,
        height: FEATURED_IMAGE_HEIGHT,
        fit: 'cover',
        position: 'centre',
      })
      .jpeg({ quality: 82, mozjpeg: true })
      .toBuffer();
    return { buffer: transformed, mimeType: 'image/jpeg' };
  }

  const isLabelLogo = usage === 'label-logo';
  const transformed = await image
    .resize({
      width: isLabelLogo ? LABEL_LOGO_MAX_WIDTH : LOGO_MAX_SIZE,
      height: isLabelLogo ? LABEL_LOGO_MAX_HEIGHT : LOGO_MAX_SIZE,
      fit: 'inside',
      withoutEnlargement: true,
    })
    .png()
    .toBuffer();
  return { buffer: transformed, mimeType: 'image/png' };
}

async function fetchImageSource(
  fetcher: typeof fetch,
  url: string
): Promise<{ contentType: string; source: Buffer }> {
  try {
    return await withAbortTimeout(
      SOURCE_FETCH_TIMEOUT_MS,
      `Source image ${url} timed out`,
      async (signal) => {
        const response = await fetcher(url, {
          headers: { Accept: 'image/webp, image/jpeg, image/png, image/gif, image/svg+xml' },
          signal,
        });
        const contentType = assertImageResponse(response, url);
        const source = await readImageSource(response, url);
        return { contentType, source };
      }
    );
  } catch (error) {
    if (error instanceof PermanentGenerationError || error instanceof TransientGenerationError) {
      throw error;
    }
    throw new TransientGenerationError(`Failed to fetch source image ${url}`, 'image_fetch', {
      cause: error,
    });
  }
}

function isTransientHttpStatus(status: number): boolean {
  return status === 408 || status === 429 || status >= 500;
}

function assertSuccessfulResponse(response: Response, url: string): void {
  if (response.ok) {
    return;
  }
  const ErrorType = isTransientHttpStatus(response.status)
    ? TransientGenerationError
    : PermanentGenerationError;
  throw new ErrorType(
    `Source image ${url} returned HTTP ${response.status}`,
    `image_http_${response.status}`
  );
}

function requireImageContentType(response: Response, url: string): string {
  const contentType = response.headers.get('content-type') ?? '';
  if (!contentType.toLowerCase().startsWith('image/')) {
    throw new PermanentGenerationError(
      `Source image ${url} has invalid content type ${contentType || '<missing>'}`,
      'invalid_image_content_type'
    );
  }
  return contentType;
}

function assertContentLength(response: Response, url: string): void {
  const contentLengthHeader = response.headers.get('content-length');
  const contentLength = contentLengthHeader ? Number.parseInt(contentLengthHeader, 10) : 0;
  if (Number.isFinite(contentLength) && contentLength > MAX_FETCHED_IMAGE_BYTES) {
    throw new PermanentGenerationError(`Source image ${url} is too large`, 'source_image_too_large');
  }
}

function assertImageResponse(response: Response, url: string): string {
  assertSuccessfulResponse(response, url);
  const contentType = requireImageContentType(response, url);
  assertContentLength(response, url);
  return contentType;
}

async function readImageSource(response: Response, url: string): Promise<Buffer> {
  try {
    return Buffer.from(await response.arrayBuffer());
  } catch (error) {
    throw new TransientGenerationError(`Failed to read source image ${url}`, 'image_fetch', {
      cause: error,
    });
  }
}

function assertSourceImageSize(source: Buffer, url: string): void {
  if (source.length === 0) {
    throw new PermanentGenerationError(`Source image ${url} is empty`, 'empty_source_image');
  }
  if (source.length > MAX_FETCHED_IMAGE_BYTES) {
    throw new PermanentGenerationError(`Source image ${url} is too large`, 'source_image_too_large');
  }
}

async function processImageDataUrl(
  source: Buffer,
  url: string,
  usage: ImageUsage,
  transform: typeof transformFetchedImage
): Promise<string> {
  const processed = await transform(source, usage);
  const dataUrl = `data:${processed.mimeType};base64,${processed.buffer.toString('base64')}`;
  const dataUrlBytes = Buffer.byteLength(dataUrl, 'utf8');
  if (dataUrlBytes > MAX_DATA_URL_BYTES[usage]) {
    throw new PermanentGenerationError(
      `Processed ${usage} image ${url} is too large`,
      'processed_image_too_large'
    );
  }
  return dataUrl;
}

export async function fetchImageAsDataUrl(
  url: string,
  usage: ImageUsage,
  fetcher: typeof fetch = fetch,
  transform: typeof transformFetchedImage = transformFetchedImage
): Promise<string> {
  const { source } = await fetchImageSource(fetcher, url);
  assertSourceImageSize(source, url);

  return processImageDataUrl(source, url, usage, transform);
}
