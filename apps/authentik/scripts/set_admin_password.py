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
user.set_password(password)
user.email = email
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
