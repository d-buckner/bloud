import os
from authentik.core.models import User

try:
    user = User.objects.get(username='akadmin')
    user.set_password(os.environ['BLOUD_ADMIN_PASSWORD'])
    user.email = os.environ['BLOUD_ADMIN_EMAIL']
    user.save()
    print('OK')
except User.DoesNotExist:
    print('OK')  # User will be created by bootstrap
except Exception as e:
    print(f'ERROR: {e}')
