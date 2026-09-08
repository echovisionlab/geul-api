import { OgEntityType } from '@echovisionlab/geul-event';
import { describe, expect, it } from 'vitest';
import { GENERATION_ID, claimed } from './claim.test-fixture.js';
import { validateClaim } from './claim.js';

const LOCALE_ENTITIES = [
  OgEntityType.POST,
  OgEntityType.PAGE,
  OgEntityType.WORK,
  OgEntityType.ARTIST,
  OgEntityType.FORM,
  OgEntityType.SERIES,
  OgEntityType.PRIVACY,
  OgEntityType.TERMS,
] as const;

const ENTITY_ENTITIES = [
  OgEntityType.LABEL,
  OgEntityType.RELEASE,
  OgEntityType.SITE,
] as const;

describe('OG claim target scope', () => {
  it.each(LOCALE_ENTITIES)('accepts locale scope for locale entity %s', (entityType) => {
    const result = validateClaim(GENERATION_ID, claimed({
      target: {
        $typeName: 'api.manage.v1.OgGenerationTarget',
        entityType,
        entityId: 'entity-1',
        scope: {
          case: 'locale',
          value: { $typeName: 'api.manage.v1.OgLocaleTarget', locale: 'ko' },
        },
      },
    }));

    expect(result).toMatchObject({ entityType, entityId: 'entity-1' });
  });

  it.each(ENTITY_ENTITIES)('accepts entity scope for global entity %s', (entityType) => {
    const result = validateClaim(GENERATION_ID, claimed({
      target: {
        $typeName: 'api.manage.v1.OgGenerationTarget',
        entityType,
        entityId: 'entity-1',
        scope: { case: 'entity', value: { $typeName: 'api.manage.v1.OgEntityTarget' } },
      },
    }));

    expect(result).toMatchObject({ entityType, entityId: 'entity-1' });
  });

  it.each([OgEntityType.WORK, OgEntityType.ARTIST])(
    'rejects entity scope for localized entity %s',
    (entityType) => {
      expect(() => validateClaim(GENERATION_ID, claimed({
        target: {
          $typeName: 'api.manage.v1.OgGenerationTarget',
          entityType,
          entityId: 'entity-1',
          scope: { case: 'entity', value: { $typeName: 'api.manage.v1.OgEntityTarget' } },
        },
      }))).toThrow('Locale-aware OG target has no locale');
    }
  );

  it('keeps Program Event, Email, and Menu outside the OG target contract', () => {
    const entityTypes = OgEntityType as unknown as Record<string, unknown>;
    expect(entityTypes.PROGRAM_EVENT).toBeUndefined();
    expect(entityTypes.EMAIL).toBeUndefined();
    expect(entityTypes.MENU).toBeUndefined();
  });
});
