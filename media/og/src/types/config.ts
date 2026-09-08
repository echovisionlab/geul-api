// OG Image configuration for homepage (logo-centered, no title)
export interface HomeOgImageConfig {
  darkBackground: string;
  logo: {
    width: number;
    height: number;
  };
  // Fallback when no logo is set
  siteTitle: {
    fontSize: number;
    fontWeight: number;
    color: string;
  };
}

// OG Image configuration for content (post/page/work)
export interface ContentOgImageConfig {
  darkBackground: string;
  title: {
    maxLength: number;
    fontSizeThreshold: number;
    fontSizeLarge: number;
    fontSizeSmall: number;
    fontWeight: number;
    color: string;
    lineHeight: number;
    padding: { top: number; right: number; bottom: number; left: number };
  };
  logo: {
    width: number;
    height: number;
    position: { bottom: number; right: number };
  };
  siteTitle: {
    fontSize: number;
    fontWeight: number;
    color: string;
    opacity: number;
  };
}

// Label OG is a dedicated logo-only surface. Label names remain in metadata
// and are never rasterized into this image.
export interface LabelOgImageConfig {
  darkBackground: string;
  logo: {
    width: number;
    height: number;
  };
}

// Default values
export const DEFAULT_HOME_OG_CONFIG: HomeOgImageConfig = {
  darkBackground: '#1A1B1E',
  logo: {
    width: 200,
    height: 200,
  },
  siteTitle: {
    fontSize: 64,
    fontWeight: 700,
    color: '#ffffff',
  },
};

export const DEFAULT_CONTENT_OG_CONFIG: ContentOgImageConfig = {
  darkBackground: '#1A1B1E',
  title: {
    maxLength: 80,
    fontSizeThreshold: 40,
    fontSizeLarge: 56,
    fontSizeSmall: 48,
    fontWeight: 700,
    color: '#ffffff',
    lineHeight: 1.3,
    padding: { top: 60, right: 80, bottom: 60, left: 80 },
  },
  logo: {
    width: 48,
    height: 48,
    position: { bottom: 32, right: 40 },
  },
  siteTitle: {
    fontSize: 18,
    fontWeight: 600,
    color: '#ffffff',
    opacity: 0.9,
  },
};

export const DEFAULT_LABEL_OG_CONFIG: LabelOgImageConfig = {
  darkBackground: '#1A1B1E',
  logo: {
    width: 720,
    height: 315,
  },
};

// OG image settings from site_settings
export interface OgImageSettings {
  siteTitle: string;
  primaryColor: string;
  logoSvg?: string;
  featuredImageDataUrl?: string;
}

// Environment configuration
export interface Env {
  HOST: string;
  PORT: number;
  OG_GENERATE_WORKERS: number;
  OG_SHUTDOWN_TIMEOUT_MS: number;
  DATABASE_DSN: string;
  S3_ENDPOINT: string;
  S3_MEDIA_BUCKET: string;
  S3_REGION: string;
  S3_ACCESS_KEY_ID: string;
  S3_SECRET_ACCESS_KEY: string;
  BACKEND_URL: string;
  TOKEN_SIGNING_SECRET: string;
}
