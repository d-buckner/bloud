# SPDX-License-Identifier: AGPL-3.0-only
import os
from authentik.core.models import User, Group

username = 'admin'
password = os.environ.get('BLOUD_ADMIN_PASSWORD', 'password')
email = os.environ.get('BLOUD_ADMIN_EMAIL', 'admin@localhost.local')

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
    #
    # set_password only mutates this object: it stores the hash on the
    # instance (and bumps password_change_date, emitting the
    # password_changed signal). The save is what persists it. Without it the
    # row keeps an empty password column, has_usable_password() still reports
    # True, and no password ever authenticates as admin: the Authentik login
    # flow, the LDAP outpost's bind flow, and every app that binds as this
    # user all fail with "Invalid password".
    user.set_password(password)
    user.save()
else:
    # Self-heal the legacy default ("admin@localhost"): SSO apps validate
    # identity emails with an RFC-style validator that requires a TLD, so
    # the old default breaks OIDC login. Operator-set emails are untouched.
    if user.email in ('', 'admin@localhost'):
        user.email = email
        user.save()
        print(f'OK: updated admin email to {email}')

try:
    group = Group.objects.get(name='authentik Admins')
    group.users.add(user)
except Group.DoesNotExist:
    pass

if created:
    print(f'OK: created admin user {username}')
else:
    print(f'OK: admin user {username} exists')
