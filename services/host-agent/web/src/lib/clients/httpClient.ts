/**
 * HTTP Client - Base utilities for API communication
 *
 * Handles common concerns: response checking, error handling, JSON parsing.
 */

export interface HttpError {
	status: number;
	message: string;
}

export interface RequestOptions extends Omit<RequestInit, 'body'> {
	body?: Record<string, any>;
}

/**
 * Make an HTTP request with standard error handling
 *
 * @throws HttpError on non-2xx responses
 */
export async function request<T>(url: string, options: RequestOptions = {}): Promise<T> {
	const { body, headers, ...rest } = options;

	const res = await fetch(url, {
		...rest,
		headers: {
			...(body !== undefined && { 'Content-Type': 'application/json' }),
			...headers
		},
		body: body !== undefined ? JSON.stringify(body) : undefined
	});

	if (!res.ok) {
		const data = await res.json().catch(() => ({}));
		const error: HttpError = {
			status: res.status,
			message: data.error || `Request failed: ${res.status}`
		};
		throw error;
	}

	// Handle empty responses
	const text = await res.text();
	if (!text) return undefined as T;

	const contentType = res.headers.get('content-type') ?? '';
	if (!contentType.includes('application/json')) {
		throw { status: res.status, message: 'Server returned an unexpected response' } as HttpError;
	}

	try {
		return JSON.parse(text) as T;
	} catch {
		throw { status: res.status, message: 'Invalid response from server' } as HttpError;
	}
}

/**
 * Make a GET request
 */
export function get<T>(url: string, options?: Omit<RequestOptions, 'method'>): Promise<T> {
	return request<T>(url, { ...options, method: 'GET' });
}

/**
 * Make a POST request
 */
export function post<T>(url: string, body?: Record<string, any>, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
	return request<T>(url, { ...options, method: 'POST', body });
}

/**
 * Make a PUT request
 */
export function put<T>(url: string, body?: Record<string, any>, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
	return request<T>(url, { ...options, method: 'PUT', body });
}

/**
 * Make a PATCH request
 */
export function patch<T>(url: string, body?: Record<string, any>, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
	return request<T>(url, { ...options, method: 'PATCH', body });
}

/**
 * Make a DELETE request
 */
export function del<T>(url: string, options?: Omit<RequestOptions, 'method'>): Promise<T> {
	return request<T>(url, { ...options, method: 'DELETE' });
}
