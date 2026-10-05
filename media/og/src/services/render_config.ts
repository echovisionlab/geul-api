import type { OgRenderConfigSnapshot } from '@echovisionlab/geul-event';
import type {
  ContentOgImageConfig,
  HomeOgImageConfig,
  LabelOgImageConfig,
} from '../types/config.js';
import {
  DEFAULT_CONTENT_OG_CONFIG,
  DEFAULT_HOME_OG_CONFIG,
  DEFAULT_LABEL_OG_CONFIG,
} from '../types/config.js';

function objectValue(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

export function getSnapshotConfigs(snapshot: OgRenderConfigSnapshot): {
  home: HomeOgImageConfig;
  content: ContentOgImageConfig;
  label: LabelOgImageConfig;
} {
  const root = objectValue(snapshot.ogImageConfig);
  const home = objectValue(root.home);
  const content = objectValue(root.content);
  const contentTitle = objectValue(content.title);
  const contentLogo = objectValue(content.logo);
  return {
    home: {
      ...DEFAULT_HOME_OG_CONFIG,
      ...home,
      logo: { ...DEFAULT_HOME_OG_CONFIG.logo, ...objectValue(home.logo) },
      siteTitle: { ...DEFAULT_HOME_OG_CONFIG.siteTitle, ...objectValue(home.siteTitle) },
    } as HomeOgImageConfig,
    content: {
      ...DEFAULT_CONTENT_OG_CONFIG,
      ...content,
      title: {
        ...DEFAULT_CONTENT_OG_CONFIG.title,
        ...contentTitle,
        padding: {
          ...DEFAULT_CONTENT_OG_CONFIG.title.padding,
          ...objectValue(contentTitle.padding),
        },
      },
      logo: {
        ...DEFAULT_CONTENT_OG_CONFIG.logo,
        ...contentLogo,
        position: {
          ...DEFAULT_CONTENT_OG_CONFIG.logo.position,
          ...objectValue(contentLogo.position),
        },
      },
      siteTitle: {
        ...DEFAULT_CONTENT_OG_CONFIG.siteTitle,
        ...objectValue(content.siteTitle),
      },
    } as ContentOgImageConfig,
    label: DEFAULT_LABEL_OG_CONFIG,
  };
}
