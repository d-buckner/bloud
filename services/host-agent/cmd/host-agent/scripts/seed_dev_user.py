import os
from authentik.core.models import User, Group

user, created = User.objects.get_or_create(
    username='admin',
    defaults={
        'name': 'Admin',
        'is_active': True,
        'path': 'users',
    }
)
user.set_password(os.environ['BLOUD_DEV_PASSWORD'])
user.save()
try:
    group = Group.objects.get(name='authentik Admins')
    group.users.add(user)
except Group.DoesNotExist:
    pass
if created:
    print('OK: created admin user')
else:
    print('OK: admin user exists')
