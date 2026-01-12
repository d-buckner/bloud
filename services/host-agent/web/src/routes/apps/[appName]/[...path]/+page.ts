import type { PageLoad } from './$types';

export const load: PageLoad = ({ params }) => {
	return {
		appName: params.appName,
		path: params.path ?? ''
	};
};
