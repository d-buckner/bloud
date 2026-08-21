// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
export const TEST_CREDS = {
  USERNAME: process.env.BLOUD_E2E_USERNAME ?? 'admin',
  PASSWORD: process.env.BLOUD_E2E_PASSWORD ?? 'password',
} as const;
