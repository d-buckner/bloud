# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Daniel Buckner
import os, time
from authentik.core.models import Token, User, Group

# Get or create a dedicated service account for the API token
user, _ = User.objects.get_or_create(
    username='bloud-api',
    defaults={
        'name': 'Bloud API Service Account',
        'type': 'internal_service_account',
        'path': 'users',
        'is_active': True,
    }
)

# Wait for authentik Admins group (created by blueprints on first boot)
group = None
for _ in range(60):
    try:
        group = Group.objects.get(name='authentik Admins')
        break
    except Group.DoesNotExist:
        time.sleep(2)

if group is None:
    print('ERROR: authentik Admins group not found after 120s')
else:
    group.users.add(user)
    # Create or update the API token
    token, created = Token.objects.get_or_create(
        identifier='bloud-api-token',
        defaults={
            'user': user,
            'key': os.environ['BLOUD_TOKEN_KEY'],
            'intent': 'api',
            'expiring': False,
            'description': 'Bloud host-agent API token',
        }
    )
    if not created:
        needs_save = False
        if token.user != user:
            token.user = user
            needs_save = True
        if token.key != os.environ['BLOUD_TOKEN_KEY']:
            token.key = os.environ['BLOUD_TOKEN_KEY']
            needs_save = True
        if token.intent != 'api':
            token.intent = 'api'
            needs_save = True
        if needs_save:
            token.save()
    print('OK')
