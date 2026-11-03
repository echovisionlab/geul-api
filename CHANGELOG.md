# Changelog

## [5.0.1](https://github.com/echovisionlab/geul-api/compare/v5.0.0...v5.0.1) (2026-11-03)


### Bug Fixes

* serve Member criteria RPCs under their contract names ([#225](https://github.com/echovisionlab/geul-api/issues/225)) ([9378d34](https://github.com/echovisionlab/geul-api/commit/9378d34fd14ef3c7cc7890f34351db69a4685fe1))

## [5.0.0](https://github.com/echovisionlab/geul-api/compare/v4.5.0...v5.0.0) (2026-11-03)


### ⚠ BREAKING CHANGES

* move audience segments into the member domain as Member criteria ([#223](https://github.com/echovisionlab/geul-api/issues/223))

### Code Refactoring

* move audience segments into the member domain as Member criteria ([#223](https://github.com/echovisionlab/geul-api/issues/223)) ([d7d280d](https://github.com/echovisionlab/geul-api/commit/d7d280df2e1b335e8e88824e87b84944d647ba7c))

## [4.5.0](https://github.com/echovisionlab/geul-api/compare/v4.4.0...v4.5.0) (2026-11-03)


### Features

* **mcp:** find text with an outline search and replace it in place ([#221](https://github.com/echovisionlab/geul-api/issues/221)) ([bb4b120](https://github.com/echovisionlab/geul-api/commit/bb4b120b748addba2f2ca1fcae6b455ca481e724))

## [4.4.0](https://github.com/echovisionlab/geul-api/compare/v4.3.0...v4.4.0) (2026-11-03)


### Features

* **mcp:** read documents in one page and keep formatting on plain-text edits ([#219](https://github.com/echovisionlab/geul-api/issues/219)) ([5ea3f9e](https://github.com/echovisionlab/geul-api/commit/5ea3f9e01e7900454793ed9cb415a4741ddc7744))

## [4.3.0](https://github.com/echovisionlab/geul-api/compare/v4.2.1...v4.3.0) (2026-11-03)


### Features

* **mcp:** Email Layout wrapper markup tools and Form password attempt budget ([#217](https://github.com/echovisionlab/geul-api/issues/217)) ([1d6a6a9](https://github.com/echovisionlab/geul-api/commit/1d6a6a99c48c8dd7e863a58aa8e92cc6ad7ce651))

## [4.2.1](https://github.com/echovisionlab/geul-api/compare/v4.2.0...v4.2.1) (2026-11-03)

### Bug Fixes

* **authentication:** restore email OTP delivery during reauthentication with an existing session ([#215](https://github.com/echovisionlab/geul-api/pull/215))

## [4.2.0](https://github.com/echovisionlab/geul-api/compare/v4.1.8...v4.2.0) (2026-11-02)


### Features

* **auth:** build a relationship snapshot from its mutation descriptor ([96fd774](https://github.com/echovisionlab/geul-api/commit/96fd7742aec5d1ff20e7eab16f72fa9a4dcbc589))
* **collaborationconflict:** answer collaboration conflicts without the transport ([beb4e95](https://github.com/echovisionlab/geul-api/commit/beb4e9546fad3b1ec4eaf840a8bb58e73ed78cf6))
* **domainerr:** answer a stale optimistic write as aborted ([8d7b257](https://github.com/echovisionlab/geul-api/commit/8d7b25713f84003bf924f2f7484e079944106fbd))
* **errors:** add the transport-free use-case error and map it at the boundary ([dba58c2](https://github.com/echovisionlab/geul-api/commit/dba58c26245bd1ea190bd3f7c4bb9b3bfc53cd5a))
* **errors:** read use-case outcomes where code branches on them ([c47481d](https://github.com/echovisionlab/geul-api/commit/c47481d642f40cd0fee697c3de0366d794a9660c))
* **failurecause:** carry private values and identifiers on the error ([8798483](https://github.com/echovisionlab/geul-api/commit/8798483c0e1d26822ee43041017fd8210494ed5c))
* **failurecause:** record server faults behind client answers at Error ([fdb03fa](https://github.com/echovisionlab/geul-api/commit/fdb03fab3868e781820603d73aed869eb971ab87))
* **identityread:** own read access to Kratos identities in one package ([cb2e3ec](https://github.com/echovisionlab/geul-api/commit/cb2e3ec715378da050a3a36ade230aa62ac97c7d))
* **identitystate:** mark the inactive principal denial of RequireFreshCan ([bcd9506](https://github.com/echovisionlab/geul-api/commit/bcd9506bac4ff2dd287b66fe6002b3a9aa2f81c4))
* **identitystate:** mark the inactive principal denial of RequireFreshCan ([55a11fc](https://github.com/echovisionlab/geul-api/commit/55a11fcb22e883ec4ca06d9197455e9a741e2f37))
* **identitystate:** run a fenced flow's writes as committed steps ([b3a7ac5](https://github.com/echovisionlab/geul-api/commit/b3a7ac5e5e096a42d4d65fcbb980671ca42d9495))
* **mutation:** add ReadCurrent to units of work ([1c0dedc](https://github.com/echovisionlab/geul-api/commit/1c0dedcbe0a38603c0ddf3d7f1400455c96c9b71))
* **mutation:** add the Fenced access and ReadLocked on units of work ([61ea46c](https://github.com/echovisionlab/geul-api/commit/61ea46c6fa5943fb380e8535896dcd098929dd2d))
* **mutation:** append several audit records for one change ([2874853](https://github.com/echovisionlab/geul-api/commit/287485344a8d5db44c547ce05bda33c35c012748))
* **mutation:** append several audit records of one change ([d1f4961](https://github.com/echovisionlab/geul-api/commit/d1f49616b5c5a405766565669b5a296bdda88bca))
* **mutation:** append the audit records of one change that touches several entities ([3db6284](https://github.com/echovisionlab/geul-api/commit/3db62847dd69b38c8bb7f602c3ed233ddf1bddbc))
* **mutation:** audit version checkpoints, read locked snapshots and recognize answered errors ([57ac64f](https://github.com/echovisionlab/geul-api/commit/57ac64fa78d332284c3c04b9d87b30bae63899ba))
* **mutation:** authorize against a locked target state and recognize denials ([850dbf3](https://github.com/echovisionlab/geul-api/commit/850dbf3ff8f1ab4dede256054b03e7cbd90f3162))
* **mutation:** lock domain roots before access, append several audit records, retry outcome ([6ee28d4](https://github.com/echovisionlab/geul-api/commit/6ee28d44c39ba08c905b50df97b8106c790290f9))
* **mutation:** lock the target before access, authorize internal callers and read one snapshot ([c5dc491](https://github.com/echovisionlab/geul-api/commit/c5dc491592bd85870a8e5492010a9e8f7e30e3c9))
* **mutation:** lock the target before access, authorize internal callers and read one snapshot ([dbfcbc3](https://github.com/echovisionlab/geul-api/commit/dbfcbc348f0575d019a5927172aa211e192e70ab))
* **mutation:** name several audit records for one mutation ([24125f7](https://github.com/echovisionlab/geul-api/commit/24125f70521294413395844a081cbcdb0b9d326d))
* **mutation:** read current through a unit of work ([5c75491](https://github.com/echovisionlab/geul-api/commit/5c754910609636bf1d0aacdaa6a07946c2b928e6))
* **mutation:** read one snapshot that locks the rows it checks ([47cd64b](https://github.com/echovisionlab/geul-api/commit/47cd64b9ecb3fd9cdf167bbf239865f33cde364f))
* **mutation:** read one snapshot that takes row locks ([5e772ed](https://github.com/echovisionlab/geul-api/commit/5e772ed7decba04d98f83e0efcb28aaf22e4c71b))
* **mutation:** read under row locks without a read-only snapshot ([e2d2108](https://github.com/echovisionlab/geul-api/commit/e2d2108e917595c6b4642bde72125155ddf929c4))
* **mutation:** recognize answered failures and name version records ([3f22d85](https://github.com/echovisionlab/geul-api/commit/3f22d858a863a5391ee91e5ad87ed040c8312686))
* **mutation:** run domain mutations through one fixed pipeline ([02bb53b](https://github.com/echovisionlab/geul-api/commit/02bb53b41f861cd86c40767db988a8d2278fa202))


### Bug Fixes

* **account:** answer an unban of an account pending deletion as a precondition ([df45bd2](https://github.com/echovisionlab/geul-api/commit/df45bd2e78f4862cde402790b9bb807648cc61e1))
* **account:** fence credential completion before identity reads ([2e3464a](https://github.com/echovisionlab/geul-api/commit/2e3464a0cbe27e3ba37466145a0361c6ca573a9f))
* **account:** seed member email fixtures with the database clock ([8dfab7d](https://github.com/echovisionlab/geul-api/commit/8dfab7db1e0daddf7831b5c072ebf3f201d1c74c))
* **aidocument:** report a missing ancestor parent instead of a parent cycle ([f702435](https://github.com/echovisionlab/geul-api/commit/f702435ae31ed7e6914c553fbbd8945452e7c052))
* **aidocument:** store relation item fields by the item kind's ownership ([4835a69](https://github.com/echovisionlab/geul-api/commit/4835a6904b494e637048108e1734848ceebd960d))
* **ai:** reject an administrator's metadata generation without a target ([aaeb529](https://github.com/echovisionlab/geul-api/commit/aaeb52970eccb261a5701206cd8f6bbdb2b73ffc))
* **artist:** request no OG image for a deleted target locale ([61f781b](https://github.com/echovisionlab/geul-api/commit/61f781b9c93aeb33030a2cd3c3789d082d42692d))
* **audience:** compute has_more of the authenticated access list without overflow ([2db626f](https://github.com/echovisionlab/geul-api/commit/2db626f1f26b2495357cba65fc29997425f9f43a))
* **audience:** reject an ineligible excluded Member as invalid input ([593e749](https://github.com/echovisionlab/geul-api/commit/593e74971bbf9b2ef0562bdef52783e6d6066c93))
* **authentication:** answer Kratos flow lookup outages with 503 ([67b558d](https://github.com/echovisionlab/geul-api/commit/67b558d9fafb7ca2b196ac00f2e0daa6402b4d16))
* **auth:** reject partial and negative Kratos identity counts ([7c77b72](https://github.com/echovisionlab/geul-api/commit/7c77b721fde6d3afd9834345376ccf35d2f5dfd5))
* **campaign:** answer Internal for stored account roles SpiceDB cannot resolve ([fa6a1ac](https://github.com/echovisionlab/geul-api/commit/fa6a1ac5922202c2299e5e275954c3014b6c5bda))
* **campaign:** wait on lock waiters and results, not wall-clock bounds ([22e8bc6](https://github.com/echovisionlab/geul-api/commit/22e8bc62906b699f3b83f2b70e225dac7e007db3))
* **dberrors:** classify a restricted foreign key delete as a violation ([6db5608](https://github.com/echovisionlab/geul-api/commit/6db5608480312e468bff368d7113512be2fd835b))
* **emailauthoring:** audit a template layout change only when the update makes one ([08d7b07](https://github.com/echovisionlab/geul-api/commit/08d7b0747bf7a28a19eda58035699e350bafa7fe))
* **emailauthoring:** read a template's document in a snapshot that may lock ([422033a](https://github.com/echovisionlab/geul-api/commit/422033aa8d03f329edf42ab14b1a5e93436be45a))
* **emaildelivery:** authorize mail adapter updates before loading the row ([95e93ab](https://github.com/echovisionlab/geul-api/commit/95e93ab489140ee35baef6ef9829f9113caf1e3e))
* **emaildelivery:** close adapters built for verification and test sends ([6d8b9cb](https://github.com/echovisionlab/geul-api/commit/6d8b9cba359c961c44658217e3f1f19e252e723e))
* **emaildelivery:** keep stored mail adapter secrets on admin updates ([c8d4297](https://github.com/echovisionlab/geul-api/commit/c8d42973ea968debba5804e1beb056c5b011aebb))
* **emaildelivery:** require an administrator to read an email suppression ([fc36b7a](https://github.com/echovisionlab/geul-api/commit/fc36b7a25b65926be32cfdbf65a443608aadb666))
* **emaildelivery:** verify a mail adapter only when its config changes ([c77e77f](https://github.com/echovisionlab/geul-api/commit/c77e77fd9fcf55f32112feaea9f2e5da44d4acdb))
* **email:** keep layout unit markers out of raw-text and RCDATA elements ([1158e5d](https://github.com/echovisionlab/geul-api/commit/1158e5dcc965ba93e4c6c173d3ea801955b3a4c0))
* **email:** preserve provider configuration error labels ([52c1d49](https://github.com/echovisionlab/geul-api/commit/52c1d49bef122044190fdcc7def8fbc7afdccf38))
* **email:** read explicit empty layout text values that precede a comment ([22755ee](https://github.com/echovisionlab/geul-api/commit/22755ee0a031c1ee4ae3c9074e8830e0e4d28fc4))
* **email:** read explicit empty layout text values that precede an inline element ([5c3fb92](https://github.com/echovisionlab/geul-api/commit/5c3fb927b720be3c097521a6f500c83a7cbc993d))
* **entityparticipant:** take the parent graph lock before any entity row ([fcad0db](https://github.com/echovisionlab/geul-api/commit/fcad0db81b085c0410f631535c9d8c6352116d84))
* **errors:** classify a use-case error by its outermost answer ([92673e7](https://github.com/echovisionlab/geul-api/commit/92673e7e6f9f5424ce5abfd45b8c8a4391252097))
* **errors:** classify malformed UUID inputs across MCP tools ([b2a8d8a](https://github.com/echovisionlab/geul-api/commit/b2a8d8afccde3caadafc01708a65ada9e3ed627b))
* **filemedia:** check a delivery's shared owner once per request ([0885300](https://github.com/echovisionlab/geul-api/commit/08853008b839a0e2fcd7755e4c09a2ad3f5a4148))
* **filemedia:** keep ingest phases on the advisory lock connection ([838a2cc](https://github.com/echovisionlab/geul-api/commit/838a2cc5ed983e265fc320afa21af9c66cedbdf3))
* **filemedia:** keep scoped delivery reads in the owner transaction ([46691c2](https://github.com/echovisionlab/geul-api/commit/46691c259cbcd5a336f1dcd3b13e7efbb64745cb))
* **filemedia:** mask use-case denials of private File owners ([0d85006](https://github.com/echovisionlab/geul-api/commit/0d85006d21d5022d769f6bc484a36b2fe95d4f9e))
* **filemediatest:** use PostgreSQL timestamp precision in contracts ([7981748](https://github.com/echovisionlab/geul-api/commit/798174870147ede8d4985768d76a6ae9e4f4e520))
* **form:** compute has_more of the admin Form list without overflow ([21f827c](https://github.com/echovisionlab/geul-api/commit/21f827cc0e9213d2bb17692a53460f85dcfd56a0))
* **geoip:** cache successful lookup misses ([6054d8d](https://github.com/echovisionlab/geul-api/commit/6054d8d07e444d349ff86dc213aee2934a84d02c))
* keep the delivery trace on shutdown requeues and session close ([a816584](https://github.com/echovisionlab/geul-api/commit/a816584c9ba43af7a9142b4288a20c60f476ba6c))
* **legal:** discard stale and source-locale translation candidates ([4982487](https://github.com/echovisionlab/geul-api/commit/49824876bd43c843d17ddd96ad8e90660fe58515))
* **legal:** look up a new version's source locale outside its transaction ([4feca6b](https://github.com/echovisionlab/geul-api/commit/4feca6bc52ede25775843cfcb7e5756e3ff3091a))
* **legaltest:** bound notice dispatch time by the schedule command ([d48ccb3](https://github.com/echovisionlab/geul-api/commit/d48ccb3c8bcd26a95709674f6c8c57ede44869ed))
* **localization:** isolate optional source locale lookups ([6b0b0d9](https://github.com/echovisionlab/geul-api/commit/6b0b0d9733df589ebffef900919277c6ad402ba5))
* **localization:** merge optional locale lookup recovery ([11ecf75](https://github.com/echovisionlab/geul-api/commit/11ecf75bb64ef17fa52e889b447b81c5aa8b4e17))
* **maptheme:** store a false show_area_labels on create and copy ([ae3a4a9](https://github.com/echovisionlab/geul-api/commit/ae3a4a9da8138c48db5c41a997fec95e519f659f))
* **mcp:** reject file_id in document_featured_image_delete ([fb49a0b](https://github.com/echovisionlab/geul-api/commit/fb49a0b7423e41f933f60dd65afd45435096801d))
* **mcp:** reject work credit arguments the tool does not advertise ([9432ed9](https://github.com/echovisionlab/geul-api/commit/9432ed9b038207fe55d55627cf34442aecabae21))
* **mcp:** reject work credit group arguments the tool does not advertise ([639314e](https://github.com/echovisionlab/geul-api/commit/639314e51a1a00d1a092722b3fbd966ed02bce23))
* **mediaasset:** answer Internal for corrupt stored download account roles ([516405a](https://github.com/echovisionlab/geul-api/commit/516405a35b15872785d455cb0ffc9359c4d38a1f))
* **member:** answer a duplicate Member tag name as AlreadyExists ([ae0105d](https://github.com/echovisionlab/geul-api/commit/ae0105de90c1444b6c241d4bb48d6ec060ff9377))
* **menu:** check a new Menu's name trimmed, after authorization ([6f1f10b](https://github.com/echovisionlab/geul-api/commit/6f1f10ba379f78a79820b855916ba1e01cda5590))
* **menu:** return no result from a validate-mode AI document mutation ([cc9d3de](https://github.com/echovisionlab/geul-api/commit/cc9d3de2a3caef0a102ce859ca4adba972bd7a5f))
* **mq:** keep e-mail command values out of the caller's failure record ([a7382a4](https://github.com/echovisionlab/geul-api/commit/a7382a47723441e08de4a0017273266ae31e137e))
* **og:** render the featured image a run verified when its file ID has spaces ([ffff7ca](https://github.com/echovisionlab/geul-api/commit/ffff7ca945569c4f0fd5d5cf6ca8e74c9209a1d2))
* **page:** attribute a collaborative layout save to its contributor ([468abc1](https://github.com/echovisionlab/geul-api/commit/468abc111057d510ac0f8cce8422b615cbf267ce))
* **page:** audit source-locale AI document edits ([5f36335](https://github.com/echovisionlab/geul-api/commit/5f36335e3d1531ab557ad23d021edb3052e6c7bf))
* **page:** compute has_more of the admin Page list without overflow ([3e716b6](https://github.com/echovisionlab/geul-api/commit/3e716b6cc5f2da3e4c03bc351e82d112ab170472))
* **page:** report a draft homepage Page as not found ([bb97331](https://github.com/echovisionlab/geul-api/commit/bb9733128c0b5096bf048d003ec4b8b327895e15))
* **page:** retry a collaboration room load that lost a session race ([824c1af](https://github.com/echovisionlab/geul-api/commit/824c1afccad452b73ed789e1832a344dedf8f8ff))
* **post:** audit AI document edits ([37c4290](https://github.com/echovisionlab/geul-api/commit/37c4290c1bbf55af1e1024506be915c89ba29465))
* **post:** audit document taxonomy metadata changes ([a75a011](https://github.com/echovisionlab/geul-api/commit/a75a011b55b38dab81e14fc23f711bf33aa78645))
* **post:** audit Post comment mutations ([1445e07](https://github.com/echovisionlab/geul-api/commit/1445e077a0024b5a8df7c6d493b6e15c7502ab41))
* **post:** publish document taxonomy content updates ([9621f22](https://github.com/echovisionlab/geul-api/commit/9621f222121961003927c78094c310965883f9a8))
* **posttest:** match inserted timestamps to PostgreSQL precision ([9a534cc](https://github.com/echovisionlab/geul-api/commit/9a534ccf51a49fdae8cd19ba22876434efe57f8f))
* **programevent:** audit source-locale AI document edits ([32ed776](https://github.com/echovisionlab/geul-api/commit/32ed7768b719da8ea2b57c59e2bda2c47f4b4f72))
* **programevent:** keep the cause of a revision conflict found while storing ([8505a1a](https://github.com/echovisionlab/geul-api/commit/8505a1a6edd2c37b78cbe89612d54da8e9626fd3))
* **publiccontent:** recover failed policy lookups inside snapshots ([6377b52](https://github.com/echovisionlab/geul-api/commit/6377b52c9e423674ce5b6d1e753049f52f81c4ba))
* **referencecatalog:** bound tag and format values by their column length ([c112782](https://github.com/echovisionlab/geul-api/commit/c1127820dbe5f8e4869e003a9fd0552b72ca40ae))
* **referencecatalog:** compute has_more of the Client lists without overflow ([caa4e3f](https://github.com/echovisionlab/geul-api/commit/caa4e3fc5f1d68795f0ae95c645e7eb663f665a9))
* **referencecatalog:** match a Client search query literally ([28b8dcc](https://github.com/echovisionlab/geul-api/commit/28b8dcc578b0bfc83308c70aa34514a50aa547e3))
* **referencecatalog:** name the requested value in update conflicts ([0a1cf65](https://github.com/echovisionlab/geul-api/commit/0a1cf6546d7855b6b11bf85566db8ad735bfc68d))
* **referencecatalog:** validate format writes like the other reference kinds ([91570c4](https://github.com/echovisionlab/geul-api/commit/91570c4a15d2e161e0d63162ce265e26c82b1197))
* **release:** audit source Block batches from Collaboration ([fd760de](https://github.com/echovisionlab/geul-api/commit/fd760de0c5a3a8300f30b36360ae85a56a2f0526))
* **release:** audit source locale metadata edits ([887d24f](https://github.com/echovisionlab/geul-api/commit/887d24fda8704bbcf6ed082a72602b8ad0f68787))
* **release:** compare Track credit roles by value ([43017af](https://github.com/echovisionlab/geul-api/commit/43017af138981da079b00ff5548fb1c01b783bfe))
* **release:** keep the cause of a revision conflict found while storing ([1ec9415](https://github.com/echovisionlab/geul-api/commit/1ec9415b17d7af9d6b55d054612f252573362c30))
* **release:** let an empty source credit note in an AI patch remove it ([b8dff35](https://github.com/echovisionlab/geul-api/commit/b8dff35867673a8451d57a0ef7fc83ba09cd80d0))
* **release:** let an empty target credit note fall back to the source ([7aef138](https://github.com/echovisionlab/geul-api/commit/7aef138f1cb10948aa111b93a7421761c8f8f537))
* **release:** lock contributor Members without an aggregate ([ff33efc](https://github.com/echovisionlab/geul-api/commit/ff33efcdaca47931083e2474e64700a30dde1251))
* **release:** report a missing Release on delete as not found ([b92f5ef](https://github.com/echovisionlab/geul-api/commit/b92f5efdbfb0e5ad2acc15d72d837762ef392ab5))
* **release:** signal a Release date change ([431105f](https://github.com/echovisionlab/geul-api/commit/431105f70fd1ce371b4f7efba660ec43b5e01d05))
* **release:** trim target locale titles like source titles ([9c59fb2](https://github.com/echovisionlab/geul-api/commit/9c59fb23d4bf1a8370cdc689a6ae42b23c2896c1))
* **series:** insert Posts through the AI document without a revision conflict ([8c181ae](https://github.com/echovisionlab/geul-api/commit/8c181aef520980be27043fe1481090c2ac6c0ef2))
* **series:** run the Post relation authority test as an audited member request ([dec160b](https://github.com/echovisionlab/geul-api/commit/dec160bc5529a4d2dee4a5a8549e28ec7b7eeee7))
* **series:** write Post membership through a Post-owned port ([78ce2c1](https://github.com/echovisionlab/geul-api/commit/78ce2c1e1650ddf04296063b52c4b4ea7dca4e08))
* **server:** log a consumer readiness failure once at startup ([6d9eddc](https://github.com/echovisionlab/geul-api/commit/6d9eddc8f71d703f7b4054e8cc98b15f4f78804a))
* **sharelink:** bound a requested expiry at the precision share_link stores ([dbc9b49](https://github.com/echovisionlab/geul-api/commit/dbc9b4902e31cf6623e89360e06633fff9b55cfb))
* **sitesettings:** merge pool-based optional asset reads ([5ae61e8](https://github.com/echovisionlab/geul-api/commit/5ae61e83515fd97dac4fd9c8affafd08d088e7cb))
* **sitesettings:** read optional assets through a pool port ([9177301](https://github.com/echovisionlab/geul-api/commit/91773017c8e8e3fa608f8f45aa34153d1d5421e9))
* **test:** isolate asset preflight from ancestor checkouts ([32325ff](https://github.com/echovisionlab/geul-api/commit/32325fffdb4e41051eb0d4934a5a2dde32d6696e))
* **test:** isolate integration path fixtures from ancestor checkouts ([df574ec](https://github.com/echovisionlab/geul-api/commit/df574ec428e43d1a5533459952563bd32568ed1d))
* **testutil:** stamp seeded Members with the database clock ([392c5a5](https://github.com/echovisionlab/geul-api/commit/392c5a5e7482dff6caf1785ebd2d02ab8d99b910))
* **translation:** keep runtime policy reads free of writes ([a9be3bc](https://github.com/echovisionlab/geul-api/commit/a9be3bc20de210964a7cacc895176edae6a96b79))
* **translation:** read back the XLIFF files the export writes ([37b5e25](https://github.com/echovisionlab/geul-api/commit/37b5e253bdb18816c01a2c8d988258bd3e1dc1c6))
* **work:** audit source locale AI document edits ([185a072](https://github.com/echovisionlab/geul-api/commit/185a0726b6c3429b18f2cac885df72fa2d6f0817))
* **work:** keep the cause of a revision conflict found while storing ([1a48c2e](https://github.com/echovisionlab/geul-api/commit/1a48c2e985a595f6a943fff0ecaf2c465f6ecdb4))
* **work:** leave a Work unchanged when its Map Place is already the requested one ([c0bbb11](https://github.com/echovisionlab/geul-api/commit/c0bbb11345b914855b1933e6c3b7ba6a9af53af9))
* **work:** trim target locale titles like source titles ([5059e90](https://github.com/echovisionlab/geul-api/commit/5059e902b11d05950fef26f4237dc5b1f310b02c))

## [4.1.8](https://github.com/echovisionlab/geul-api/compare/v4.1.7...v4.1.8) (2026-10-31)


### Bug Fixes

* return mutation timestamps at the precision PostgreSQL stores ([#209](https://github.com/echovisionlab/geul-api/issues/209)) ([f075aca](https://github.com/echovisionlab/geul-api/commit/f075aca89e2c316d6e74cfb4d8f85182fb54b14d))

## [4.1.7](https://github.com/echovisionlab/geul-api/compare/v4.1.6...v4.1.7) (2026-10-31)


### Bug Fixes

* lint and isolated parallel integration tests in CI, and the bugs they found ([#208](https://github.com/echovisionlab/geul-api/issues/208)) ([790acfe](https://github.com/echovisionlab/geul-api/commit/790acfe8940e1b6008f7388acd62f62210cefc50))

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
