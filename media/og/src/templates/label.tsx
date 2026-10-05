import type { LabelOgImageConfig, OgImageSettings } from '../types/config.js';
import { DEFAULT_LABEL_OG_CONFIG } from '../types/config.js';

interface LabelOgTemplateProps {
  settings: OgImageSettings;
  config?: LabelOgImageConfig;
}

export function LabelOgTemplate({
  settings,
  config = DEFAULT_LABEL_OG_CONFIG,
}: LabelOgTemplateProps) {
  return (
    <div
      style={{
        width: '100%',
        height: '100%',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        backgroundColor: config.darkBackground,
      }}
    >
      {settings.featuredImageDataUrl ? (
        <img
          src={settings.featuredImageDataUrl}
          width={config.logo.width}
          height={config.logo.height}
          style={{ objectFit: 'contain' }}
          alt=""
        />
      ) : null}
    </div>
  );
}
