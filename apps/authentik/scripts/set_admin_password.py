# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Daniel Buckner
import os
from authentik.core.models import User, Group

username = 'admin'
password = os.environ.get('BLOUD_ADMIN_PASSWORD', 'password')
email = os.environ.get('BLOUD_ADMIN_EMAIL', 'admin@localhost')

user, created = User.objects.get_or_create(
    username=username,
    defaults={
        'name': 'Admin',
        'email': email,
        'is_active': True,
        'path': 'users',
    },
)
if created:
    # Only set the password when the user is created. Re-applying the
    # bootstrap password on every start would overwrite the password the
    # operator chose in Bloud's setup wizard and lock them out on restart.
    user.set_password(password)
    user.save()

try:
    group = Group.objects.get(name='authentik Admins')
    group.users.add(user)
except Group.DoesNotExist:
    pass

if created:
    print(f'OK: created admin user {username}')
else:
    print(f'OK: admin user {username} exists')
