# Changelog

## [4.1.6](https://github.com/echovisionlab/geul-api/compare/v4.1.5...v4.1.6) (2026-10-30)


### Bug Fixes

* **mail:** working verification links and localized email-change mail ([#206](https://github.com/echovisionlab/geul-api/issues/206)) ([c3ee638](https://github.com/echovisionlab/geul-api/commit/c3ee638336f3cd69b0141b8122515b161298437b))

## [4.1.5](https://github.com/echovisionlab/geul-api/compare/v4.1.4...v4.1.5) (2026-10-30)


### Bug Fixes

* **mail:** stop double sends, false opt-outs and silent test sends ([#204](https://github.com/echovisionlab/geul-api/issues/204)) ([c99b3b5](https://github.com/echovisionlab/geul-api/commit/c99b3b5a98e732ad482e858bcc5aa67b285559e1))

## [4.1.4](https://github.com/echovisionlab/geul-api/compare/v4.1.3...v4.1.4) (2026-10-30)


### Bug Fixes

* **mail:** keep mail retryable through outages and abandoned requests ([#202](https://github.com/echovisionlab/geul-api/issues/202)) ([6f7ed93](https://github.com/echovisionlab/geul-api/commit/6f7ed93fcfd7789802c543d2684a526349f60901))

## [4.1.3](https://github.com/echovisionlab/geul-api/compare/v4.1.2...v4.1.3) (2026-10-30)


### Bug Fixes

* **mail:** complete the SNS signing certificate chain from AIA ([#200](https://github.com/echovisionlab/geul-api/issues/200)) ([aac595d](https://github.com/echovisionlab/geul-api/commit/aac595dadaf9678a86e04f21db1c1ba4746f6e17))

## [4.1.2](https://github.com/echovisionlab/geul-api/compare/v4.1.1...v4.1.2) (2026-10-29)


### Bug Fixes

* **auth:** forward resend on registration code follow-ups ([#198](https://github.com/echovisionlab/geul-api/issues/198)) ([d48085f](https://github.com/echovisionlab/geul-api/commit/d48085fb0c326f1a65a84c0769a663197796f877))

## [4.1.1](https://github.com/echovisionlab/geul-api/compare/v4.1.0...v4.1.1) (2026-10-29)


### Bug Fixes

* **member:** offer a deleted Member's nickname as available ([#196](https://github.com/echovisionlab/geul-api/issues/196)) ([b7c6d7d](https://github.com/echovisionlab/geul-api/commit/b7c6d7d78c2f6d0052b382030695040117344051))

## [4.1.0](https://github.com/echovisionlab/geul-api/compare/v4.0.6...v4.1.0) (2026-10-29)


### Features

* **mcp:** read and set Page access policies ([#194](https://github.com/echovisionlab/geul-api/issues/194)) ([13fa0e3](https://github.com/echovisionlab/geul-api/commit/13fa0e3ff72dad1d3da2b06c4599ccc50b2dfd00))

## [4.0.6](https://github.com/echovisionlab/geul-api/compare/v4.0.5...v4.0.6) (2026-10-28)


### Bug Fixes

* **og:** flush telemetry before the worker exits ([#191](https://github.com/echovisionlab/geul-api/issues/191)) ([682446d](https://github.com/echovisionlab/geul-api/commit/682446d4490129d735f5ab6fcadbc834f0648a97))

## [4.0.5](https://github.com/echovisionlab/geul-api/compare/v4.0.4...v4.0.5) (2026-10-28)


### Bug Fixes

* **og:** move to OpenTelemetry SDK 3.0 and instrument ESM imports ([#189](https://github.com/echovisionlab/geul-api/issues/189)) ([85a65b0](https://github.com/echovisionlab/geul-api/commit/85a65b0ccfb3eed062d4c3fee68f42d2b9db266d))

## [4.0.4](https://github.com/echovisionlab/geul-api/compare/v4.0.3...v4.0.4) (2026-10-28)


### Bug Fixes

* localize artist Work titles and refresh dependencies ([#187](https://github.com/echovisionlab/geul-api/issues/187)) ([cf0e390](https://github.com/echovisionlab/geul-api/commit/cf0e3906eee4bb041a29ec89ce1c8bf83168e0f9))

## [4.0.3](https://github.com/echovisionlab/geul-api/compare/v4.0.2...v4.0.3) (2026-10-28)


### Bug Fixes

* read Work and Artist OG only from the source-locale translation ([#185](https://github.com/echovisionlab/geul-api/issues/185)) ([30ac3fb](https://github.com/echovisionlab/geul-api/commit/30ac3fb649eb0034043eb13f194ddb353dd02442))

## [4.0.2](https://github.com/echovisionlab/geul-api/compare/v4.0.1...v4.0.2) (2026-10-28)


### Bug Fixes

* **og:** render SVG logos at the target size ([#183](https://github.com/echovisionlab/geul-api/issues/183)) ([09bf796](https://github.com/echovisionlab/geul-api/commit/09bf796fdec66fb91d3029a3e28c8710517e9dda))

## [4.0.1](https://github.com/echovisionlab/geul-api/compare/v4.0.0...v4.0.1) (2026-10-27)


### Bug Fixes

* accept logo slot and contact email changes in settings audit ([#181](https://github.com/echovisionlab/geul-api/issues/181)) ([9d31d93](https://github.com/echovisionlab/geul-api/commit/9d31d9386402b4f03b977136c7fa74263c358337))

## [4.0.0](https://github.com/echovisionlab/geul-api/compare/v3.4.1...v4.0.0) (2026-10-21)


### ⚠ BREAKING CHANGES

* Run display-size-migrate --apply --writers-stopped and --verify before this API starts.

### Features

* read displaySize and convert stored previewWidth ([#178](https://github.com/echovisionlab/geul-api/issues/178)) ([3ba7322](https://github.com/echovisionlab/geul-api/commit/3ba7322e2986894d7c3de57f8a8f396fabea0e52))

## [3.4.1](https://github.com/echovisionlab/geul-api/compare/v3.4.0...v3.4.1) (2026-10-21)


### Bug Fixes

* **deps:** update dependencies, toolchain and base images to latest ([#175](https://github.com/echovisionlab/geul-api/issues/175)) ([2e9d848](https://github.com/echovisionlab/geul-api/commit/2e9d84850067337e65bfe4fd6672ce3f643c5938))

## [3.4.0](https://github.com/echovisionlab/geul-api/compare/v3.3.7...v3.4.0) (2026-10-21)


### Features

* read link previews for rich-text link cards ([#173](https://github.com/echovisionlab/geul-api/issues/173)) ([4e9d88f](https://github.com/echovisionlab/geul-api/commit/4e9d88ff584ee9cc0b8506815e53bcb7f87796d9))

## [3.3.7](https://github.com/echovisionlab/geul-api/compare/v3.3.6...v3.3.7) (2026-10-20)


### Bug Fixes

* **mcp:** keep the page table-of-contents setting in layout edits ([#171](https://github.com/echovisionlab/geul-api/issues/171)) ([b36f14e](https://github.com/echovisionlab/geul-api/commit/b36f14e0b906f2036863ea06518b79e3ee2bd713))

## [3.3.6](https://github.com/echovisionlab/geul-api/compare/v3.3.5...v3.3.6) (2026-10-20)


### Bug Fixes

* request a locale's OG image when an AI document saves its title ([#169](https://github.com/echovisionlab/geul-api/issues/169)) ([18f1ca6](https://github.com/echovisionlab/geul-api/commit/18f1ca62983c76498cb3f036ddb22cfa8c4c698f))

## [3.3.5](https://github.com/echovisionlab/geul-api/compare/v3.3.4...v3.3.5) (2026-10-20)


### Bug Fixes

* retry Page reads that lose a repeatable read race to a writer ([#167](https://github.com/echovisionlab/geul-api/issues/167)) ([1d7358b](https://github.com/echovisionlab/geul-api/commit/1d7358b41bcf57cc2db7a1ae6e208c92739a5860))

## [3.3.4](https://github.com/echovisionlab/geul-api/compare/v3.3.3...v3.3.4) (2026-10-20)


### Bug Fixes

* start a request context for trusted callers without a request ID ([#166](https://github.com/echovisionlab/geul-api/issues/166)) ([7bc70a1](https://github.com/echovisionlab/geul-api/commit/7bc70a1a739b10526539547904bf9564eba4d5d8))
* use one log level policy for operation failures ([#164](https://github.com/echovisionlab/geul-api/issues/164)) ([5034257](https://github.com/echovisionlab/geul-api/commit/50342573f09119be84d071eca763e447d8616320))

## [3.3.3](https://github.com/echovisionlab/geul-api/compare/v3.3.2...v3.3.3) (2026-10-20)


### Bug Fixes

* make failure logs diagnosable and keep expected outcomes out of warnings ([#161](https://github.com/echovisionlab/geul-api/issues/161)) ([b4ae5ea](https://github.com/echovisionlab/geul-api/commit/b4ae5ea2bac64b1344e1adf5445fda44e2ee978a))
* read Work credit artist names from the artist's source locale ([#162](https://github.com/echovisionlab/geul-api/issues/162)) ([d053904](https://github.com/echovisionlab/geul-api/commit/d0539048e9bc189d215f51f6700ed0ba23411ec1))

## [3.3.2](https://github.com/echovisionlab/geul-api/compare/v3.3.1...v3.3.2) (2026-10-19)


### Bug Fixes

* show the default locale before the original when a translation is missing ([#159](https://github.com/echovisionlab/geul-api/issues/159)) ([115d08e](https://github.com/echovisionlab/geul-api/commit/115d08e4052df3ac0f65d9353ccc0f655c535cd1))

## [3.3.1](https://github.com/echovisionlab/geul-api/compare/v3.3.0...v3.3.1) (2026-10-19)


### Bug Fixes

* expose create-tool arguments to clients without schema conditionals ([#156](https://github.com/echovisionlab/geul-api/issues/156)) ([41b770f](https://github.com/echovisionlab/geul-api/commit/41b770f2dc3ff75c58dca49d66d5ad15c4d35909))
* keep every MCP tool input schema a plain object at the root ([#158](https://github.com/echovisionlab/geul-api/issues/158)) ([f1977f2](https://github.com/echovisionlab/geul-api/commit/f1977f237115b2928c7166c0662f558b7a2013de))

## [3.3.0](https://github.com/echovisionlab/geul-api/compare/v3.2.1...v3.3.0) (2026-10-19)


### Features

* author headings and tables through the document tools ([#154](https://github.com/echovisionlab/geul-api/issues/154)) ([18a455e](https://github.com/echovisionlab/geul-api/commit/18a455e1d5e9029641032e66628f6561743b416f))

## [3.2.1](https://github.com/echovisionlab/geul-api/compare/v3.2.0...v3.2.1) (2026-10-18)


### Bug Fixes

* keep checkpoints from an earlier block catalog as history ([#152](https://github.com/echovisionlab/geul-api/issues/152)) ([fd6dd23](https://github.com/echovisionlab/geul-api/commit/fd6dd23795391f8dbbe8624a70363093641027b2))

## [3.2.0](https://github.com/echovisionlab/geul-api/compare/v3.1.1...v3.2.0) (2026-10-18)


### Features

* serve email fonts from the Geul font system and drop the font proxy ([#150](https://github.com/echovisionlab/geul-api/issues/150)) ([7cbac12](https://github.com/echovisionlab/geul-api/commit/7cbac1258870915ad2f7866a53d54a362ef89e63))

## [3.1.1](https://github.com/echovisionlab/geul-api/compare/v3.1.0...v3.1.1) (2026-10-18)


### Bug Fixes

* authorize comment reads and persist outline visibility ([#147](https://github.com/echovisionlab/geul-api/issues/147)) ([a9e12a2](https://github.com/echovisionlab/geul-api/commit/a9e12a295ca597fe472bf5164d0b32e3c454b402))

## [3.1.0](https://github.com/echovisionlab/geul-api/compare/v3.0.0...v3.1.0) (2026-10-18)


### Features

* accept xs and xl collection item sizes ([#145](https://github.com/echovisionlab/geul-api/issues/145)) ([f090f7e](https://github.com/echovisionlab/geul-api/commit/f090f7e9e72a76db87bec07424a49110977af52a))

## [3.0.0](https://github.com/echovisionlab/geul-api/compare/v2.0.5...v3.0.0) (2026-10-17)


### ⚠ BREAKING CHANGES

* full, symbol and email site logos with underscore binding keys ([#143](https://github.com/echovisionlab/geul-api/issues/143))

### Features

* full, symbol and email site logos with underscore binding keys ([#143](https://github.com/echovisionlab/geul-api/issues/143)) ([5f9b5d9](https://github.com/echovisionlab/geul-api/commit/5f9b5d938b2a4fbaec717314aa77a7a9b140f50d))

## [2.0.5](https://github.com/echovisionlab/geul-api/compare/v2.0.4...v2.0.5) (2026-10-17)


### Bug Fixes

* allow library SVG images in site logo settings ([#141](https://github.com/echovisionlab/geul-api/issues/141)) ([d204730](https://github.com/echovisionlab/geul-api/commit/d20473060beb5fbf876e9b869eb318470b838b26))

## [2.0.4](https://github.com/echovisionlab/geul-api/compare/v2.0.3...v2.0.4) (2026-10-17)


### Bug Fixes

* complete content responses and authorized editor media ([#138](https://github.com/echovisionlab/geul-api/issues/138)) ([5390770](https://github.com/echovisionlab/geul-api/commit/5390770b1a8cff0ff95ecd2b392c718485952c94))

## [2.0.3](https://github.com/echovisionlab/geul-api/compare/v2.0.2...v2.0.3) (2026-10-17)


### Bug Fixes

* **translation:** keep AI response references owned by the source ([#136](https://github.com/echovisionlab/geul-api/issues/136)) ([d427492](https://github.com/echovisionlab/geul-api/commit/d4274926e8361fff6b5bbf84a7655fa09751bd9c))

## [2.0.2](https://github.com/echovisionlab/geul-api/compare/v2.0.1...v2.0.2) (2026-10-17)


### Bug Fixes

* **email:** keep footer separators inline when removing formatting breaks ([#133](https://github.com/echovisionlab/geul-api/issues/133)) ([b15aa10](https://github.com/echovisionlab/geul-api/commit/b15aa10e1de798380450cfe4287722429372d124))

## [2.0.1](https://github.com/echovisionlab/geul-api/compare/v2.0.0...v2.0.1) (2026-10-16)


### Bug Fixes

* **migration:** validate observed history snapshots for contact cutover ([#130](https://github.com/echovisionlab/geul-api/issues/130)) ([9a64f22](https://github.com/echovisionlab/geul-api/commit/9a64f2270e0ab0a115e1110765cbf2eec0f30ffd))

## [2.0.0](https://github.com/echovisionlab/geul-api/compare/v1.10.0...v2.0.0) (2026-10-16)


### ⚠ BREAKING CHANGES

* require canonical contacts and migrate stored content ([#128](https://github.com/echovisionlab/geul-api/issues/128))

### Features

* require canonical contacts and migrate stored content ([#128](https://github.com/echovisionlab/geul-api/issues/128)) ([d65f5f5](https://github.com/echovisionlab/geul-api/commit/d65f5f5105f36a01eb1866b493194be30648ec28))


### Bug Fixes

* preserve dependency and cleanup failures at service boundaries ([#126](https://github.com/echovisionlab/geul-api/issues/126)) ([3ad5ca2](https://github.com/echovisionlab/geul-api/commit/3ad5ca204a91f583eceff7a2031b54470fa184b9))
* preserve email formatting and isolate application tests ([#123](https://github.com/echovisionlab/geul-api/issues/123)) ([7155b62](https://github.com/echovisionlab/geul-api/commit/7155b62b9be580753f2187ec85fb2b8eed9dcd94))
* **translation:** enqueue jobs within the owning transaction ([#129](https://github.com/echovisionlab/geul-api/issues/129)) ([63eb32b](https://github.com/echovisionlab/geul-api/commit/63eb32b334e38a768c58fceeb4eafc71355929c5))

## [1.10.0](https://github.com/echovisionlab/geul-api/compare/v1.9.1...v1.10.0) (2026-10-16)


### Features

* **mcp:** manage site settings and mail adapter senders ([#121](https://github.com/echovisionlab/geul-api/issues/121)) ([a4145ef](https://github.com/echovisionlab/geul-api/commit/a4145ef3e6473b5d9104b140b2ed027d9a986d43))
* **site-variables:** migrate content to site variables and fix email delivery ([#119](https://github.com/echovisionlab/geul-api/issues/119)) ([c281c0d](https://github.com/echovisionlab/geul-api/commit/c281c0df0638643a3ffaa7e7d63cd628a1c67f28))


### Bug Fixes

* **logging:** keep sanitized failure causes in production logs ([#120](https://github.com/echovisionlab/geul-api/issues/120)) ([7e56ffd](https://github.com/echovisionlab/geul-api/commit/7e56ffd7a9f30fe6735e01f0a0dbc15dbc54ad13))

## [1.9.1](https://github.com/echovisionlab/geul-api/compare/v1.9.0...v1.9.1) (2026-10-16)


### Bug Fixes

* **docker:** authenticate private npm installs through pnpm config ([#117](https://github.com/echovisionlab/geul-api/issues/117)) ([cdb7908](https://github.com/echovisionlab/geul-api/commit/cdb79087e44e345ebb0626d8108bf1e325f3465f))

## [1.9.0](https://github.com/echovisionlab/geul-api/compare/v1.8.0...v1.9.0) (2026-10-16)


### Features

* **opendiscogs:** verify member PATs for Oathkeeper ([#115](https://github.com/echovisionlab/geul-api/issues/115)) ([5e8946c](https://github.com/echovisionlab/geul-api/commit/5e8946caf95fa0da12540d7caebfdcdae7ec79ac))

## [1.8.0](https://github.com/echovisionlab/geul-api/compare/v1.7.1...v1.8.0) (2026-10-15)


### Features

* **collaboration:** durable collaboration API ([#109](https://github.com/echovisionlab/geul-api/issues/109)) ([bd95a9a](https://github.com/echovisionlab/geul-api/commit/bd95a9a15a24ba70ddafb616b774b5617fa412a5))

## [1.7.1](https://github.com/echovisionlab/geul-api/compare/v1.7.0...v1.7.1) (2026-10-09)


### Bug Fixes

* preserve translation inline authority and finish legal OG work ([#104](https://github.com/echovisionlab/geul-api/issues/104)) ([46e76a6](https://github.com/echovisionlab/geul-api/commit/46e76a64a59f2a232022368e3197015b8926df82))

## [1.7.0](https://github.com/echovisionlab/geul-api/compare/v1.6.1...v1.7.0) (2026-10-09)


### Features

* **mcp:** support legal policy CRUD across every lifecycle ([#102](https://github.com/echovisionlab/geul-api/issues/102)) ([0448e26](https://github.com/echovisionlab/geul-api/commit/0448e2689b480fce8ed6862fd77b6f2a9a0fae22))

## [1.6.1](https://github.com/echovisionlab/geul-api/compare/v1.6.0...v1.6.1) (2026-10-08)


### Bug Fixes

* preserve actionable MCP and HTTP error classifications ([#100](https://github.com/echovisionlab/geul-api/issues/100)) ([bca0e4b](https://github.com/echovisionlab/geul-api/commit/bca0e4b7357260265f377e857d18cbea5c210e0d))

## [1.6.0](https://github.com/echovisionlab/geul-api/compare/v1.5.2...v1.6.0) (2026-10-08)


### Features

* **mcp:** expose authorized menu discovery and site locations ([#98](https://github.com/echovisionlab/geul-api/issues/98)) ([e0ebccf](https://github.com/echovisionlab/geul-api/commit/e0ebccf8750f86b63609370eebd46bbee6e2d813))

## [1.5.2](https://github.com/echovisionlab/geul-api/compare/v1.5.1...v1.5.2) (2026-10-06)


### Bug Fixes

* **media:** vary font CORS responses by origin and bypass stale cache ([#96](https://github.com/echovisionlab/geul-api/issues/96)) ([f7a604d](https://github.com/echovisionlab/geul-api/commit/f7a604dc7b0b3ba193746f2a153eb1034eeef033))

## [1.5.1](https://github.com/echovisionlab/geul-api/compare/v1.5.0...v1.5.1) (2026-10-06)


### Bug Fixes

* **media:** preserve poster image delivery aspect ratio ([#94](https://github.com/echovisionlab/geul-api/issues/94)) ([f36edf3](https://github.com/echovisionlab/geul-api/commit/f36edf33139dfa27295b40ed25fb0a6af8d3dc3e))

## [1.5.0](https://github.com/echovisionlab/geul-api/compare/v1.4.3...v1.5.0) (2026-10-06)


### Features

* **page:** enforce audiences and support independent tool embeds ([#92](https://github.com/echovisionlab/geul-api/issues/92)) ([318d3d0](https://github.com/echovisionlab/geul-api/commit/318d3d0816f48fecd4fb81de84085378ca052436))

## [1.4.3](https://github.com/echovisionlab/geul-api/compare/v1.4.2...v1.4.3) (2026-10-06)


### Bug Fixes

* **events:** reuse uploaded image assets for posters ([#90](https://github.com/echovisionlab/geul-api/issues/90)) ([4a867c3](https://github.com/echovisionlab/geul-api/commit/4a867c3579b09a26c0ba474f972863ea88664b20))

## [1.4.2](https://github.com/echovisionlab/geul-api/compare/v1.4.1...v1.4.2) (2026-10-06)


### Bug Fixes

* **mcp:** accept complete HTTPS file source URLs ([#88](https://github.com/echovisionlab/geul-api/issues/88)) ([11febec](https://github.com/echovisionlab/geul-api/commit/11febec08c854470f9d79ded12448b1e9fcb0490))

## [1.4.1](https://github.com/echovisionlab/geul-api/compare/v1.4.0...v1.4.1) (2026-10-05)


### Bug Fixes

* align media tools and CI with Node 24.21.0 ([f893e63](https://github.com/echovisionlab/geul-api/commit/f893e630eff9b57c0d47b5670f00af83cf787ee8))
* **deps:** patch asset optimizer image and glob dependencies ([1f68a66](https://github.com/echovisionlab/geul-api/commit/1f68a66d2d644915d3d3d69f1d1d4ea0040af7cf))
* **deps:** patch asset optimizer image and glob dependencies ([90bfa4a](https://github.com/echovisionlab/geul-api/commit/90bfa4a76fed96220270bf831d766b9af4fd619f))
* **deps:** refresh API and media runtime dependencies ([#87](https://github.com/echovisionlab/geul-api/issues/87)) ([ed82a58](https://github.com/echovisionlab/geul-api/commit/ed82a580c17b54e3d422ae535c27682bc245b817))

## [1.4.0](https://github.com/echovisionlab/geul-api/compare/v1.3.4...v1.4.0) (2026-10-05)


### Features

* **mcp:** add admin content and file management workflows ([#79](https://github.com/echovisionlab/geul-api/issues/79)) ([a5cc7c2](https://github.com/echovisionlab/geul-api/commit/a5cc7c2a97472384fb140a117c0ff66a70e955e6))

## [1.3.4](https://github.com/echovisionlab/geul-api/compare/v1.3.3...v1.3.4) (2026-10-05)


### Bug Fixes

* **mcp:** preserve document state and owning outcomes ([#77](https://github.com/echovisionlab/geul-api/issues/77)) ([146ab2e](https://github.com/echovisionlab/geul-api/commit/146ab2ec6a78661a0e24ae455f4975314fd1d83e))

## [1.3.3](https://github.com/echovisionlab/geul-api/compare/v1.3.2...v1.3.3) (2026-10-05)


### Bug Fixes

* **mcp:** advertise compact inputs as recursive arrays ([#75](https://github.com/echovisionlab/geul-api/issues/75)) ([65d1c03](https://github.com/echovisionlab/geul-api/commit/65d1c03bb97d00c5ac3f5d960be5ff85736e2fe1))

## [1.3.2](https://github.com/echovisionlab/geul-api/compare/v1.3.1...v1.3.2) (2026-10-05)


### Bug Fixes

* **mcp:** accept compact tuple unions in connectors ([#73](https://github.com/echovisionlab/geul-api/issues/73)) ([e538da7](https://github.com/echovisionlab/geul-api/commit/e538da7800c3422374de3cd9a24a524d12949961))

## [1.3.1](https://github.com/echovisionlab/geul-api/compare/v1.3.0...v1.3.1) (2026-10-05)


### Bug Fixes

* preserve MCP mutation intent and align tool contracts ([#70](https://github.com/echovisionlab/geul-api/issues/70)) ([9f7f54a](https://github.com/echovisionlab/geul-api/commit/9f7f54aa076e6b6d2767d53565cf236cce6fd871))

## [1.3.0](https://github.com/echovisionlab/geul-api/compare/v1.2.1...v1.3.0) (2026-10-05)


### Features

* stream upload sources for browser processing ([#69](https://github.com/echovisionlab/geul-api/issues/69)) ([793a59c](https://github.com/echovisionlab/geul-api/commit/793a59c0063e6efc8009912869d6d9357b822208))

## [1.2.1](https://github.com/echovisionlab/geul-api/compare/v1.2.0...v1.2.1) (2026-10-04)


### Bug Fixes

* allow public asset cleanup without Cloudflare ([#67](https://github.com/echovisionlab/geul-api/issues/67)) ([600b854](https://github.com/echovisionlab/geul-api/commit/600b8543c2e0f05d8c74f787858e710e21df3a3e))

## [1.2.0](https://github.com/echovisionlab/geul-api/compare/v1.1.2...v1.2.0) (2026-10-04)


### Features

* accept complete browser-prepared media uploads ([7502a84](https://github.com/echovisionlab/geul-api/commit/7502a842a707a4ff9d3c785fe5cb72993d402515))

## [1.1.2](https://github.com/echovisionlab/geul-api/compare/v1.1.1...v1.1.2) (2026-10-03)


### Bug Fixes

* **map:** align viewport and clustering with MapLibre projection ([#63](https://github.com/echovisionlab/geul-api/issues/63)) ([9683051](https://github.com/echovisionlab/geul-api/commit/9683051974d02a41cdf3b7e6dcf1fcc5cb38e79a))

## [1.1.1](https://github.com/echovisionlab/geul-api/compare/v1.1.0...v1.1.1) (2026-10-03)


### Performance Improvements

* **mcp:** reduce response allocations and clarify transport settings ([#61](https://github.com/echovisionlab/geul-api/issues/61)) ([2319cfa](https://github.com/echovisionlab/geul-api/commit/2319cfaaead562b2666e12bd515f495ff7270c5e))

## [1.1.0](https://github.com/echovisionlab/geul-api/compare/v1.0.2...v1.1.0) (2026-10-03)


### Features

* **mcp:** discover Release and Artist documents ([#60](https://github.com/echovisionlab/geul-api/issues/60)) ([5f9dbc7](https://github.com/echovisionlab/geul-api/commit/5f9dbc73ab08684058f1e03dc20034734e4552f5))
* **mcp:** recover document revision changes as sync states ([#58](https://github.com/echovisionlab/geul-api/issues/58)) ([5098819](https://github.com/echovisionlab/geul-api/commit/50988192ecd5de8702ce568d4f4b0987077c6016))

## [1.0.2](https://github.com/echovisionlab/geul-api/compare/v1.0.1...v1.0.2) (2026-10-03)


### Bug Fixes

* **mcp:** negotiate supported initialization version ([#56](https://github.com/echovisionlab/geul-api/issues/56)) ([9d3c9e2](https://github.com/echovisionlab/geul-api/commit/9d3c9e2140d81c5eec26edc45460eaa035c11a21))

## [1.0.1](https://github.com/echovisionlab/geul-api/compare/v1.0.0...v1.0.1) (2026-10-01)


### Bug Fixes

* **runtime:** preserve queue outcomes and fresh authority ([#54](https://github.com/echovisionlab/geul-api/issues/54)) ([d3221d7](https://github.com/echovisionlab/geul-api/commit/d3221d76bbf5766d53d6d79d7b81ae6da3137c5c))

## [1.0.0](https://github.com/echovisionlab/geul-api/compare/v0.5.8...v1.0.0) (2026-10-01)


### ⚠ BREAKING CHANGES

* merge editor intent under current resource locks ([#52](https://github.com/echovisionlab/geul-api/issues/52))

### Bug Fixes

* merge editor intent under current resource locks ([#52](https://github.com/echovisionlab/geul-api/issues/52)) ([49ff0ac](https://github.com/echovisionlab/geul-api/commit/49ff0accfd2c62531a6f31a1a9e25f4ca0b742d7))

## [0.5.8](https://github.com/echovisionlab/geul-api/compare/v0.5.7...v0.5.8) (2026-10-01)


### Bug Fixes

* **post:** reject stale configuration writes ([#50](https://github.com/echovisionlab/geul-api/issues/50)) ([1b78bc5](https://github.com/echovisionlab/geul-api/commit/1b78bc5ff0faef5e0eaa76dacfee7b3a510ce1f7))

## [0.5.7](https://github.com/echovisionlab/geul-api/compare/v0.5.6...v0.5.7) (2026-10-01)


### Bug Fixes

* preserve public work snapshots and batch list relations ([#48](https://github.com/echovisionlab/geul-api/issues/48)) ([5e8735a](https://github.com/echovisionlab/geul-api/commit/5e8735ac5f4b63afea7d04b41f0801d5982a3326))

## [0.5.6](https://github.com/echovisionlab/geul-api/compare/v0.5.5...v0.5.6) (2026-09-30)


### Bug Fixes

* **api:** clarify queue policies and service ownership ([#45](https://github.com/echovisionlab/geul-api/issues/45)) ([3894623](https://github.com/echovisionlab/geul-api/commit/3894623e7b006865a610b332fbc7cd852eaf61e9))

## [0.5.5](https://github.com/echovisionlab/geul-api/compare/v0.5.4...v0.5.5) (2026-09-30)


### Bug Fixes

* fence concurrent content mutations and durable worker delivery ([#42](https://github.com/echovisionlab/geul-api/issues/42)) ([b71fe5a](https://github.com/echovisionlab/geul-api/commit/b71fe5a0369c91f2fde470cf280a3d7a8e9d5a81))

## [0.5.4](https://github.com/echovisionlab/geul-api/compare/v0.5.3...v0.5.4) (2026-09-30)


### Bug Fixes

* preserve Page block and translation write integrity ([#40](https://github.com/echovisionlab/geul-api/issues/40)) ([4b12750](https://github.com/echovisionlab/geul-api/commit/4b12750cd3cd519846f6723e475abf05d1b1d79b))

## [0.5.3](https://github.com/echovisionlab/geul-api/compare/v0.5.2...v0.5.3) (2026-09-10)


### Bug Fixes

* remove obsolete media setting guard and contract checks ([#32](https://github.com/echovisionlab/geul-api/issues/32)) ([8236e1a](https://github.com/echovisionlab/geul-api/commit/8236e1a22793edc675489a1a2a27d40c9b449980))

## [0.5.2](https://github.com/echovisionlab/geul-api/compare/v0.5.1...v0.5.2) (2026-09-10)


### Bug Fixes

* accept only canonical personal access token prefix ([#30](https://github.com/echovisionlab/geul-api/issues/30)) ([7c966d4](https://github.com/echovisionlab/geul-api/commit/7c966d4e3e364043e0a3ff246e01664319c972f1))

## [0.5.1](https://github.com/echovisionlab/geul-api/compare/v0.5.0...v0.5.1) (2026-09-10)


### Bug Fixes

* issue personal access tokens with neutral prefix ([#28](https://github.com/echovisionlab/geul-api/issues/28)) ([9b2c840](https://github.com/echovisionlab/geul-api/commit/9b2c8404f82d3eaf6905ea4da3524e1aeffcb2c0))

## [0.5.0](https://github.com/echovisionlab/geul-api/compare/v0.4.1...v0.5.0) (2026-09-10)


### Features

* **http:** standardize authentication and quota response metadata ([#26](https://github.com/echovisionlab/geul-api/issues/26)) ([5749567](https://github.com/echovisionlab/geul-api/commit/57495676f2876ee3b586d7f99e9872bb606a3e72))

## [0.4.1](https://github.com/echovisionlab/geul-api/compare/v0.4.0...v0.4.1) (2026-09-10)


### Bug Fixes

* **auth:** keep PAT verification independent of consuming services ([80cb857](https://github.com/echovisionlab/geul-api/commit/80cb85736e3683b341432c198abbe929c1cfce3a))
* **auth:** make PAT verification independent of consuming services ([36a37e3](https://github.com/echovisionlab/geul-api/commit/36a37e30a3990e41c6dc082bf1e80aafe9548723))

## [0.4.0](https://github.com/echovisionlab/geul-api/compare/v0.3.0...v0.4.0) (2026-09-10)


### Features

* **auth:** verify Member API keys for the OpenDiscogs gateway ([f58cb7c](https://github.com/echovisionlab/geul-api/commit/f58cb7cf223d092199478cad5304f8d8588ed69f))
* **auth:** verify Member API keys for the OpenDiscogs gateway ([1d3d828](https://github.com/echovisionlab/geul-api/commit/1d3d8285f7a61a0573934f56cf62a1dc14d2d191))

## [0.3.0](https://github.com/echovisionlab/geul-api/compare/v0.2.2...v0.3.0) (2026-09-08)


### Features

* **content:** render Mermaid blocks and remove fixed site branding ([#18](https://github.com/echovisionlab/geul-api/issues/18)) ([21a3d1f](https://github.com/echovisionlab/geul-api/commit/21a3d1fe71988d92b7b4cc09c0d33370256eb8e1))

## [0.2.2](https://github.com/echovisionlab/geul-api/compare/v0.2.1...v0.2.2) (2026-09-08)


### Bug Fixes

* **account:** restore settings credential validation and domain diagnostics ([#16](https://github.com/echovisionlab/geul-api/issues/16)) ([bfd6a95](https://github.com/echovisionlab/geul-api/commit/bfd6a95c1eb61d5a10d0bd22ad1fc89f20fdfd0b))

## [0.2.1](https://github.com/echovisionlab/geul-api/compare/v0.2.0...v0.2.1) (2026-09-08)


### Bug Fixes

* **media:** internalize media authorization dependency ([#14](https://github.com/echovisionlab/geul-api/issues/14)) ([9c75c67](https://github.com/echovisionlab/geul-api/commit/9c75c67251ae30551f301e8b700a6e054c64b013))

## [0.2.0](https://github.com/echovisionlab/geul-api/compare/v0.1.2...v0.2.0) (2026-09-08)


### Features

* **media:** integrate existing media services into API ([#11](https://github.com/echovisionlab/geul-api/issues/11)) ([78b39c3](https://github.com/echovisionlab/geul-api/commit/78b39c3d511246bd7ea3eaf52b072a460e483c59))

## [0.1.2](https://github.com/echovisionlab/geul-api/compare/v0.1.1...v0.1.2) (2026-09-06)


### Bug Fixes

* **api:** restore music services and authorization-aware routes ([#9](https://github.com/echovisionlab/geul-api/issues/9)) ([f376675](https://github.com/echovisionlab/geul-api/commit/f376675f55ac83caf11977ad23f93845d075820c))

## [0.1.1](https://github.com/echovisionlab/geul-api/compare/v0.1.0...v0.1.1) (2026-09-02)


### Bug Fixes

* **ci:** pin integration helper images by digest ([#6](https://github.com/echovisionlab/geul-api/issues/6)) ([f3cc9d0](https://github.com/echovisionlab/geul-api/commit/f3cc9d0f32661bab798d8e25a96771005f4436ab))

## 0.1.0 (2026-09-02)


### Features

* publish initial Geul API ([2a4df33](https://github.com/echovisionlab/geul-api/commit/2a4df3309ffbab6878d1c3b285d0dd953aab110c))


### Bug Fixes

* **ci:** install ImageMagick for favicon tests ([#4](https://github.com/echovisionlab/geul-api/issues/4)) ([d459db8](https://github.com/echovisionlab/geul-api/commit/d459db8a923c694c51f02763207569a0cf33d406))

## 0.1.0

Initial public Geul API release.
