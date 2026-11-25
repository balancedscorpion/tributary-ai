import { expect, test } from '@playwright/test';

test('query provider registry includes snowflake mappings and terminology', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const registry = await import('/lib/queryProviderRegistry.ts' as string);
    const descriptor = registry.getQueryMetadataToolDescriptor('list_snowflake_databases');
    const nativeLink = registry.resolveQueryNativeJobLink('snowflake', {
      statementId: '01b12345-0200-0abc-0000-000000000000',
      accountIdentifier: 'xy12345.us-east-1',
    });
    return {
      label: registry.getQueryProviderLabel('snowflake'),
      runTool: registry.isQueryRunToolName('run_snowflake_query'),
      catalogTools: registry.getAllCatalogToolNames(),
      schemaTools: registry.getAllSchemaToolNames(),
      terms: registry.getQueryProviderMetadataTerminology('snowflake'),
      descriptor,
      nativeLink,
    };
  });

  expect(result.label).toBe('Snowflake');
  expect(result.runTool).toBe(true);
  expect(result.catalogTools).toEqual(expect.arrayContaining([
    'list_snowflake_databases',
    'list_snowflake_schemas',
    'list_snowflake_tables',
    'describe_snowflake_table',
  ]));
  expect(result.schemaTools).toContain('describe_snowflake_table');
  expect(result.terms.databases.singular).toBe('Database');
  expect(result.terms.databases.plural).toBe('Databases');
  expect(result.descriptor?.kind).toBe('databases');
  expect(result.descriptor?.term?.plural).toBe('Databases');
  expect(result.nativeLink?.id).toBe('01b12345-0200-0abc-0000-000000000000');
  expect(result.nativeLink?.url).toContain('snowflakecomputing.com');
});

test('settings migration keeps snowflake defaults and datasource state', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const { DEFAULT_SETTINGS } = await import('/constants.ts' as string);
    const { migrateSettings } = await import('/services/settingsMigration.ts' as string);

    const raw = structuredClone(DEFAULT_SETTINGS) as Record<string, any>;
    delete raw.snowflakeAccountIdentifier;
    delete raw.snowflakeWarehouse;
    delete raw.snowflakeDatabase;
    delete raw.snowflakeSchema;
    delete raw.snowflakeRole;
    delete raw.snowflakeUser;
    raw.featureFlags = { ...(raw.featureFlags || {}) };
    delete raw.featureFlags.cloudCredentialsSnowflake;
    raw.dataSources = { ...(raw.dataSources || {}) };
    delete raw.dataSources.snowflake;

    const migrated = migrateSettings(raw);
    return {
      account: migrated.snowflakeAccountIdentifier,
      warehouse: migrated.snowflakeWarehouse,
      database: migrated.snowflakeDatabase,
      schema: migrated.snowflakeSchema,
      role: migrated.snowflakeRole,
      user: migrated.snowflakeUser,
      flag: migrated.featureFlags?.cloudCredentialsSnowflake,
      defaultFlag: DEFAULT_SETTINGS.featureFlags?.cloudCredentialsSnowflake,
      datasourceEnabled: migrated.dataSources?.snowflake?.enabled,
    };
  });

  expect(result.account).toBe('');
  expect(result.warehouse).toBe('');
  expect(result.database).toBe('');
  expect(result.schema).toBe('');
  expect(result.role).toBe('');
  expect(result.user).toBe('');
  expect(result.flag).toBe(result.defaultFlag);
  expect(result.datasourceEnabled).toBe(false);
});

test('secrets status normalization maps snowflake secret booleans', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const present = await page.evaluate(async () => {
    const { normalizeSecretsStatus } = await import('/services/secretsApi.ts' as string);
    const status = normalizeSecretsStatus({
      initialized: true,
      locked: false,
      configured: {
        snowflake: true,
        snowflakeOAuthToken: true,
        snowflakeKeyPair: false,
      },
    });
    return status.present;
  });

  expect(present.snowflake_credentials).toBe(true);
  expect(present['provider:snowflake']).toBe(true);
  expect(present.snowflake_oauth_token).toBe(true);
  expect(present['provider:snowflake:oauth_token']).toBe(true);
  expect(present.snowflake_keypair).toBe(false);
  expect(present['provider:snowflake:keypair_jwt']).toBe(false);
});

test('snowflake secrets helpers send backend-compatible payloads', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const api = await import('/services/secretsApi.ts' as string);
    const calls: Array<{ url: string; method: string; body: any }> = [];
    const originalFetch = window.fetch.bind(window);

    window.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string'
        ? input
        : input instanceof URL
          ? input.toString()
          : input.url;
      const bodyText = typeof init?.body === 'string' ? init.body : '';
      calls.push({
        url,
        method: init?.method || 'GET',
        body: bodyText ? JSON.parse(bodyText) : null,
      });
      return new Response('{}', {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }) as typeof window.fetch;

    try {
      await api.setSnowflakeOAuthToken('  tok-123  ');
      await api.setSnowflakeKeyPair('  -----BEGIN PRIVATE KEY-----abc-----END PRIVATE KEY-----  ', '  pass-1  ');
      await api.clearSnowflakeSecrets();
    } finally {
      window.fetch = originalFetch;
    }

    return {
      oauthPayload: api.buildSnowflakeOAuthTokenPayload('  tok-123  '),
      keypairPayload: api.buildSnowflakeKeyPairPayload('  pem  ', '  pass-1  '),
      clearPayload: api.buildSnowflakeClearPayload(),
      calls,
    };
  });

  expect(result.oauthPayload).toEqual({
    providerId: 'snowflake',
    authType: 'oauth_token',
    oauthAccessToken: 'tok-123',
  });
  expect(result.keypairPayload).toEqual({
    providerId: 'snowflake',
    authType: 'keypair_jwt',
    privateKeyPem: 'pem',
    privateKeyPassphrase: 'pass-1',
  });
  expect(result.clearPayload).toEqual({
    providerId: 'snowflake',
    authType: 'all',
  });

  expect(result.calls).toEqual([
    {
      url: '/api/secrets/set',
      method: 'POST',
      body: {
        providerId: 'snowflake',
        authType: 'oauth_token',
        oauthAccessToken: 'tok-123',
      },
    },
    {
      url: '/api/secrets/set',
      method: 'POST',
      body: {
        providerId: 'snowflake',
        authType: 'keypair_jwt',
        privateKeyPem: '-----BEGIN PRIVATE KEY-----abc-----END PRIVATE KEY-----',
        privateKeyPassphrase: 'pass-1',
      },
    },
    {
      url: '/api/secrets/clear',
      method: 'POST',
      body: {
        providerId: 'snowflake',
        authType: 'all',
      },
    },
  ]);
});

test('snowflake api normalizers parse status, test, and metadata payloads', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const api = await import('/services/snowflakeApi.ts' as string);
    const status = api.normalizeSnowflakeStatus({
      auth_mode: 'keypair_jwt',
      account_configured: true,
      warehouse_configured: true,
      database_configured: true,
      schema_configured: true,
      role_configured: false,
      user_configured: true,
      account_identifier: 'xy12345.us-east-1',
      warehouse: 'ANALYTICS_WH',
      database: 'ANALYTICS',
      schema: 'PUBLIC',
      user: 'analyst_user',
    });
    const test = api.normalizeSnowflakeTestResult({
      ok: true,
      query_id: '01b12345',
      statement_handle: '01b12345',
      message: 'ok',
    });
    const preflight = api.normalizeSnowflakePreflightResult({
      ok: true,
      read_only: true,
      warning: 'EXPLAIN unavailable',
    });
    const tables = api.normalizeSnowflakeMetadataListResult({
      tables: [{ name: 'ORDERS' }, { table_name: 'CUSTOMERS' }],
    });
    const schema = api.normalizeSnowflakeTableSchemaResult({
      columns: [{ name: 'ORDER_ID', type: 'NUMBER' }],
    });
    return { status, test, preflight, tables, schema };
  });

  expect(result.status.authMode).toBe('keypair_jwt');
  expect(result.status.accountIdentifier).toBe('xy12345.us-east-1');
  expect(result.test.ok).toBe(true);
  expect(result.test.queryId).toBe('01b12345');
  expect(result.preflight.readOnly).toBe(true);
  expect(result.tables.items).toEqual(['ORDERS', 'CUSTOMERS']);
  expect(result.schema.columns[0].name).toBe('ORDER_ID');
});
