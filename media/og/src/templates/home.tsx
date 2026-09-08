import type { HomeOgImageConfig, OgImageSettings } from '../types/config.js';
import { DEFAULT_HOME_OG_CONFIG } from '../types/config.js';

interface HomeOgTemplateProps {
  settings: OgImageSettings;
  config?: HomeOgImageConfig;
}

export function HomeOgTemplate({ settings, config = DEFAULT_HOME_OG_CONFIG }: HomeOgTemplateProps) {
  const hasFeaturedImage = !!settings.featuredImageDataUrl;

  return (
    <div
      style={{
        width: '100%',
        height: '100%',
        display: 'flex',
        position: 'relative',
        alignItems: 'center',
        justifyContent: 'center',
        backgroundColor: config.darkBackground,
        fontFamily:
          'Noto Sans Arabic, Noto Sans Thai, Noto Sans KR, Noto Sans JP, Noto Sans SC, Noto Sans',
      }}
    >
      {hasFeaturedImage ? (
        <img
          src={settings.featuredImageDataUrl}
          alt=""
          style={{
            position: 'absolute',
            top: 0,
            left: 0,
            width: '100%',
            height: '100%',
            objectFit: 'cover',
          }}
        />
      ) : null}
      {hasFeaturedImage ? (
        <div
          style={{
            position: 'absolute',
            top: 0,
            left: 0,
            width: '100%',
            height: '100%',
            backgroundColor: 'rgba(0, 0, 0, 0.36)',
          }}
        />
      ) : null}
      {settings.logoSvg ? (
        <img
          src={settings.logoSvg}
          width={config.logo.width}
          height={config.logo.height}
          style={{ objectFit: 'contain', position: 'relative' }}
          alt=""
        />
      ) : (
        <div
          style={{
            position: 'relative',
            fontSize: `${config.siteTitle.fontSize}px`,
            fontWeight: config.siteTitle.fontWeight,
            color: config.siteTitle.color,
            textAlign: 'center',
          }}
        >
          {settings.siteTitle}
        </div>
      )}
    </div>
  );
}
