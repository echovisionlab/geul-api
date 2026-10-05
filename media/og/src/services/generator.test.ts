import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  readFileSync: vi.fn(),
  satori: vi.fn(),
  sharp: vi.fn(),
  contentTemplate: vi.fn((props: unknown) => ({ kind: 'content', props })),
  homeTemplate: vi.fn((props: unknown) => ({ kind: 'home', props })),
  labelTemplate: vi.fn((props: unknown) => ({ kind: 'label', props })),
  logger: { warn: vi.fn() },
  webp: vi.fn(),
  toBuffer: vi.fn(),
}));

vi.mock('fs', () => ({ readFileSync: mocks.readFileSync }));
vi.mock('satori', () => ({ default: mocks.satori }));
vi.mock('sharp', () => ({ default: mocks.sharp }));
vi.mock('../logger.js', () => ({ logger: mocks.logger }));
vi.mock('../templates/content.js', () => ({ ContentOgTemplate: mocks.contentTemplate }));
vi.mock('../templates/home.js', () => ({ HomeOgTemplate: mocks.homeTemplate }));
vi.mock('../templates/label.js', () => ({ LabelOgTemplate: mocks.labelTemplate }));

const settings = {
  siteTitle: 'Example Studio',
  primaryColor: '#b02d23',
  featuredImageDataUrl: 'data:image/jpeg;base64,featured',
  logoSvg: 'data:image/png;base64,logo',
};

async function importGenerator() {
  vi.resetModules();
  return import('./generator.js');
}

describe('OG generator', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.readFileSync.mockReturnValue(Buffer.from('font'));
    mocks.satori.mockResolvedValue('<svg/>');
    mocks.toBuffer.mockResolvedValue(Buffer.from('webp'));
    mocks.webp.mockReturnValue({ toBuffer: mocks.toBuffer });
    mocks.sharp.mockReturnValue({ webp: mocks.webp });
  });

  it('renders content with default config and caches loaded fonts', async () => {
    const { generateContentOgImage } = await importGenerator();

    await expect(generateContentOgImage('Title', settings)).resolves.toEqual(Buffer.from('webp'));
    await expect(generateContentOgImage('Again', settings)).resolves.toEqual(Buffer.from('webp'));

    expect(mocks.readFileSync).toHaveBeenCalledTimes(12);
    expect(mocks.contentTemplate).toHaveBeenCalledWith(
      expect.objectContaining({
        title: 'Title',
        settings,
        config: expect.objectContaining({ darkBackground: '#1A1B1E' }),
      })
    );
    expect(mocks.satori).toHaveBeenCalledWith(expect.anything(), {
      width: 1200,
      height: 630,
      fonts: expect.arrayContaining([
        expect.objectContaining({ name: 'Noto Sans', weight: 400, style: 'normal' }),
        expect.objectContaining({ name: 'Noto Sans Arabic', weight: 400, style: 'normal' }),
        expect.objectContaining({ name: 'Noto Sans Thai', weight: 400, style: 'normal' }),
      ]),
    });
    expect(mocks.webp).toHaveBeenCalledWith({ quality: 85 });
  });

  it('renders home with custom config while tolerating an unavailable font', async () => {
    mocks.readFileSync.mockImplementationOnce(() => {
      throw new Error('missing font');
    });
    const { generateHomeOgImage } = await importGenerator();
    const config = {
      darkBackground: '#000000',
      logo: { width: 100, height: 100 },
      siteTitle: { fontSize: 50, fontWeight: 700, color: '#ffffff' },
    };

    await expect(
      generateHomeOgImage({ siteTitle: 'Example Studio', primaryColor: '#fff' }, config)
    ).resolves.toEqual(Buffer.from('webp'));

    expect(mocks.logger.warn).toHaveBeenCalledWith(
      expect.objectContaining({ err: expect.any(Error) }),
      'Failed to load font'
    );
    expect(mocks.homeTemplate).toHaveBeenCalledWith({
      settings: {
        siteTitle: 'Example Studio',
        primaryColor: '#fff',
        featuredImageDataUrl: undefined,
        logoSvg: undefined,
      },
      config,
    });
  });

  it('renders a title-free Label image with the effective logo centered at the production size', async () => {
    const { generateLabelOgImage } = await importGenerator();

    await expect(generateLabelOgImage(settings)).resolves.toEqual(Buffer.from('webp'));

    expect(mocks.labelTemplate).toHaveBeenCalledWith({
      settings,
      config: {
        darkBackground: '#1A1B1E',
        logo: { width: 720, height: 315 },
      },
    });
    expect(mocks.contentTemplate).not.toHaveBeenCalled();
    expect(mocks.homeTemplate).not.toHaveBeenCalled();
  });

  it('rejects Label generation without an effective logo', async () => {
    const { generateLabelOgImage } = await importGenerator();

    await expect(
      generateLabelOgImage({ siteTitle: 'Example Studio', primaryColor: '#fff' })
    ).rejects.toThrow('requires an effective logo');
    expect(mocks.satori).not.toHaveBeenCalled();
  });

  it('drops oversized embedded images before rendering', async () => {
    const { generateContentOgImage } = await importGenerator();
    const oversized = {
      ...settings,
      featuredImageDataUrl: 'x'.repeat(1_500_001),
      logoSvg: 'x'.repeat(500_001),
    };

    await generateContentOgImage('Title', oversized);

    expect(mocks.contentTemplate).toHaveBeenCalledWith(
      expect.objectContaining({
        settings: expect.objectContaining({
          featuredImageDataUrl: undefined,
          logoSvg: undefined,
        }),
      })
    );
    expect(mocks.logger.warn).toHaveBeenCalledTimes(2);
  });

  it.each([
    ['content', (module: typeof import('./generator.js')) => module.generateContentOgImage('x', settings)],
    ['home', (module: typeof import('./generator.js')) => module.generateHomeOgImage(settings)],
    ['label', (module: typeof import('./generator.js')) => module.generateLabelOgImage(settings)],
  ])('rejects %s generation when no font can be loaded', async (_name, generate) => {
    mocks.readFileSync.mockImplementation(() => {
      throw new Error('all fonts missing');
    });
    const module = await importGenerator();

    await expect(generate(module)).rejects.toThrow('No fonts loaded for OG image generation');
  });

  it('rejects an oversized rendered SVG before invoking Sharp', async () => {
    mocks.satori.mockResolvedValue('x'.repeat(5 * 1024 * 1024 + 1));
    const { generateContentOgImage } = await importGenerator();

    await expect(generateContentOgImage('Title', settings)).rejects.toThrow(
      'Generated SVG exceeds safe size limit'
    );
    expect(mocks.sharp).not.toHaveBeenCalled();
  });
});
