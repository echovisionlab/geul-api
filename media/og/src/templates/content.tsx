import type { ContentOgImageConfig, OgImageSettings } from '../types/config.js';
import { DEFAULT_CONTENT_OG_CONFIG } from '../types/config.js';

const GRADIENT_OVERLAY =
  'linear-gradient(to top, rgba(0,0,0,0.75) 0%, rgba(0,0,0,0.4) 50%, rgba(0,0,0,0.2) 100%)';
const TITLE_TEXT_SHADOW = '0 2px 8px rgba(0,0,0,0.5)';

function truncateTitle(title: string, maxLength: number): string {
  if (title.length <= maxLength) {
    return title;
  }
  return `${title.substring(0, maxLength - 3)}...`;
}

interface ContentOgTemplateProps {
  title: string;
  settings: OgImageSettings;
  config?: ContentOgImageConfig;
}

function renderBackground(
  settings: OgImageSettings,
  hasFeaturedImage: boolean
) {
  const image = hasFeaturedImage ? (
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
  ) : null;

  return (
    <>
      {image}
      <div
        style={{
          position: 'absolute',
          top: 0,
          left: 0,
          width: '100%',
          height: '100%',
          background: hasFeaturedImage ? GRADIENT_OVERLAY : 'transparent',
        }}
      />
    </>
  );
}

function renderTitle(
  title: string,
  config: ContentOgImageConfig,
  hasFeaturedImage: boolean
) {
  const { title: titleConfig } = config;
  const fontSize =
    title.length > titleConfig.fontSizeThreshold
      ? titleConfig.fontSizeSmall
      : titleConfig.fontSizeLarge;

  return (
    <div
      style={{
        position: 'absolute',
        top: 0,
        left: 0,
        width: '100%',
        height: '100%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: `${titleConfig.padding.top}px ${titleConfig.padding.right}px ${titleConfig.padding.bottom}px ${titleConfig.padding.left}px`,
      }}
    >
      <div
        style={{
          fontSize: `${fontSize}px`,
          fontWeight: titleConfig.fontWeight,
          color: titleConfig.color,
          textAlign: 'center',
          lineHeight: titleConfig.lineHeight,
          wordBreak: 'keep-all',
          textShadow: hasFeaturedImage ? TITLE_TEXT_SHADOW : 'none',
        }}
      >
        {title}
      </div>
    </div>
  );
}

function renderBrand(settings: OgImageSettings, config: ContentOgImageConfig) {
  const { logo: logoConfig, siteTitle: siteTitleConfig } = config;
  return (
    <div
      style={{
        position: 'absolute',
        bottom: logoConfig.position.bottom,
        right: logoConfig.position.right,
        display: 'flex',
        alignItems: 'center',
      }}
    >
      {settings.logoSvg ? (
        <img
          src={settings.logoSvg}
          width={logoConfig.width}
          height={logoConfig.height}
          style={{ objectFit: 'contain' }}
          alt=""
        />
      ) : (
        <div
          style={{
            fontSize: `${siteTitleConfig.fontSize}px`,
            fontWeight: siteTitleConfig.fontWeight,
            color: siteTitleConfig.color,
            opacity: siteTitleConfig.opacity,
          }}
        >
          {settings.siteTitle}
        </div>
      )}
    </div>
  );
}

export function ContentOgTemplate({
  title,
  settings,
  config = DEFAULT_CONTENT_OG_CONFIG,
}: ContentOgTemplateProps) {
  const truncatedTitle = truncateTitle(title, config.title.maxLength);
  const hasFeaturedImage = !!settings.featuredImageDataUrl;

  return (
    <div
      style={{
        width: '100%',
        height: '100%',
        display: 'flex',
        position: 'relative',
        backgroundColor: config.darkBackground,
        fontFamily:
          'Noto Sans Arabic, Noto Sans Thai, Noto Sans KR, Noto Sans JP, Noto Sans SC, Noto Sans',
      }}
    >
      {renderBackground(settings, hasFeaturedImage)}
      {renderTitle(truncatedTitle, config, hasFeaturedImage)}
      {renderBrand(settings, config)}
    </div>
  );
}
