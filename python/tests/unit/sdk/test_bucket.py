import unittest
from unittest import mock
from unittest.mock import Mock, call, patch

from aistore.sdk.bucket import Bucket, Header
from aistore.sdk.object_iterator import ObjectIterator

from aistore.sdk.const import (
    ACT_CREATE_BCK,
    HTTP_METHOD_POST,
    ProviderAmazon,
    QParamBucketTo,
    ProviderAIS,
    ACT_MOVE_BCK,
    ACT_DESTROY_BCK,
    HTTP_METHOD_DELETE,
    QParamKeepBckMD,
    ACT_EVICT_REMOTE_BCK,
    HTTP_METHOD_HEAD,
    ACT_COPY_BCK,
    ACT_LIST,
    HTTP_METHOD_GET,
    ACT_ETL_BCK,
    QParamProvider,
    HTTP_METHOD_PUT,
    URL_PATH_BUCKETS,
)
from aistore.sdk.errors import InvalidBckProvider
from aistore.sdk.request_client import RequestClient
from aistore.sdk.types import (
    ActionMsg,
    BucketList,
    BucketEntry,
    Namespace,
)

BCK_NAME = "bucket_name"
NAMESPACE = "namespace"


# pylint: disable=too-many-public-methods,unused-variable
class TestBucket(unittest.TestCase):
    def setUp(self) -> None:
        self.mock_client = Mock(RequestClient)
        self.amz_bck = Bucket(self.mock_client, BCK_NAME, provider=ProviderAmazon)
        self.amz_bck_params = self.amz_bck.qparam.copy()
        self.ais_bck = Bucket(
            self.mock_client, BCK_NAME, namespace=Namespace(uuid="", name=NAMESPACE)
        )
        self.ais_bck_params = self.ais_bck.qparam.copy()

    def test_default_props(self):
        bucket = Bucket(self.mock_client, BCK_NAME)
        self.assertEqual({QParamProvider: ProviderAIS}, bucket.qparam)
        self.assertEqual(ProviderAIS, bucket.provider)
        self.assertIsNone(bucket.namespace)

    def test_properties(self):
        self.assertEqual(self.mock_client, self.ais_bck.client)
        expected_ns = Namespace(uuid="", name=NAMESPACE)
        client = RequestClient("test client name")
        bck = Bucket(
            client=client, name=BCK_NAME, provider=ProviderAmazon, namespace=expected_ns
        )
        self.assertEqual(client, bck.client)
        self.assertEqual(ProviderAmazon, bck.provider)
        self.assertEqual({QParamProvider: ProviderAmazon}, bck.qparam)
        self.assertEqual(BCK_NAME, bck.name)
        self.assertEqual(expected_ns, bck.namespace)

    def test_create_invalid_provider(self):
        self.assertRaises(InvalidBckProvider, self.amz_bck.create)

    def test_create_success(self):
        self.ais_bck.create()
        self.mock_client.request.assert_called_with(
            HTTP_METHOD_POST,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            json=ActionMsg(action=ACT_CREATE_BCK).dict(),
            params=self.ais_bck.qparam,
        )

    def test_rename_invalid_provider(self):
        self.assertRaises(InvalidBckProvider, self.amz_bck.rename, "new_name")

    def test_rename_success(self):
        new_bck_name = "new_bucket"
        expected_response = "rename_op_123"
        self.ais_bck_params[QParamBucketTo] = f"{ProviderAIS}/@#/{new_bck_name}/"
        mock_response = Mock()
        mock_response.text = expected_response
        self.mock_client.request.return_value = mock_response

        response = self.ais_bck.rename(new_bck_name)

        self.assertEqual(expected_response, response)
        self.mock_client.request.assert_called_with(
            HTTP_METHOD_POST,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            json=ActionMsg(action=ACT_MOVE_BCK).dict(),
            params=self.ais_bck_params,
        )
        self.assertEqual(self.ais_bck.name, new_bck_name)

    def test_delete_invalid_provider(self):
        self.assertRaises(InvalidBckProvider, self.amz_bck.delete)

    def test_delete_success(self):
        self.ais_bck.delete()
        self.mock_client.request.assert_called_with(
            HTTP_METHOD_DELETE,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            json=ActionMsg(action=ACT_DESTROY_BCK).dict(),
            params=self.ais_bck.qparam,
        )

    def test_evict_invalid_provider(self):
        self.assertRaises(InvalidBckProvider, self.ais_bck.evict)

    def test_evict_success(self):
        for keep_md in [True, False]:
            self.amz_bck_params[QParamKeepBckMD] = str(keep_md)
            self.amz_bck.evict(keep_md=keep_md)
            self.mock_client.request.assert_called_with(
                HTTP_METHOD_DELETE,
                path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
                json=ActionMsg(action=ACT_EVICT_REMOTE_BCK).dict(),
                params=self.amz_bck_params,
            )

    def test_head(self):
        mock_header = Mock()
        mock_header.headers = Header("value")
        self.mock_client.request.return_value = mock_header
        headers = self.ais_bck.head()
        self.mock_client.request.assert_called_with(
            HTTP_METHOD_HEAD,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            params=self.ais_bck.qparam,
        )
        self.assertEqual(headers, mock_header.headers)

    def test_copy_default_params(self):
        action_value = {"prefix": "", "dry_run": False, "force": False}
        self._copy_exec_assert("new_bck", ProviderAIS, action_value)

    def test_copy(self):
        prefix = "prefix-"
        dry_run = True
        force = True
        action_value = {"prefix": prefix, "dry_run": dry_run, "force": force}

        self._copy_exec_assert(
            "new_bck",
            ProviderAmazon,
            action_value,
            prefix=prefix,
            dry_run=dry_run,
            force=force,
        )

    def _copy_exec_assert(self, to_bck_name, to_provider, expected_act_value, **kwargs):
        expected_response = "copy-action-id"
        mock_response = Mock()
        mock_response.text = expected_response
        self.mock_client.request.return_value = mock_response
        self.ais_bck_params[QParamBucketTo] = f"{to_provider}/@#/{to_bck_name}/"
        expected_action = ActionMsg(
            action=ACT_COPY_BCK, value=expected_act_value
        ).dict()

        job_id = self.ais_bck.copy(to_bck_name, to_provider=to_provider, **kwargs)

        self.assertEqual(expected_response, job_id)
        self.mock_client.request.assert_called_with(
            HTTP_METHOD_POST,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            json=expected_action,
            params=self.ais_bck_params,
        )

    def test_list_objects(self):
        prefix = "prefix-"
        page_size = 0
        uuid = "1234"
        props = "name"
        continuation_token = "token"
        expected_act_value = {
            "prefix": prefix,
            "pagesize": page_size,
            "uuid": uuid,
            "props": props,
            "continuation_token": continuation_token,
        }
        self._list_objects_exec_assert(
            expected_act_value,
            prefix=prefix,
            page_size=page_size,
            uuid=uuid,
            props=props,
            continuation_token=continuation_token,
        )

    def test_list_objects_default_params(self):
        expected_act_value = {
            "prefix": "",
            "pagesize": 0,
            "uuid": "",
            "props": "",
            "continuation_token": "",
        }
        self._list_objects_exec_assert(expected_act_value)

    def _list_objects_exec_assert(self, expected_act_value, **kwargs):
        action = ActionMsg(action=ACT_LIST, value=expected_act_value).dict()

        return_val = Mock(BucketList)
        self.mock_client.request_deserialize.return_value = return_val
        result = self.ais_bck.list_objects(**kwargs)
        self.mock_client.request_deserialize.assert_called_with(
            HTTP_METHOD_GET,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            res_model=BucketList,
            json=action,
            params=self.ais_bck_params,
        )
        self.assertEqual(result, return_val)

    def test_list_objects_iter(self):
        self.assertIsInstance(
            self.ais_bck.list_objects_iter("prefix-", "obj props", 123), ObjectIterator
        )

    def test_list_all_objects(self):
        list_1_id = "123"
        list_1_cont = "cont"
        prefix = "prefix-"
        page_size = 5
        props = "name"
        expected_act_value_1 = {
            "prefix": prefix,
            "pagesize": page_size,
            "uuid": "",
            "props": props,
            "continuation_token": "",
        }
        expected_act_value_2 = {
            "prefix": prefix,
            "pagesize": page_size,
            "uuid": list_1_id,
            "props": props,
            "continuation_token": list_1_cont,
        }
        self._list_all_objects_exec_assert(
            list_1_id,
            list_1_cont,
            expected_act_value_1,
            expected_act_value_2,
            prefix=prefix,
            page_size=page_size,
            props=props,
        )

    def test_list_all_objects_default_params(self):
        list_1_id = "123"
        list_1_cont = "cont"
        expected_act_value_1 = {
            "prefix": "",
            "pagesize": 0,
            "uuid": "",
            "props": "",
            "continuation_token": "",
        }
        expected_act_value_2 = {
            "prefix": "",
            "pagesize": 0,
            "uuid": list_1_id,
            "props": "",
            "continuation_token": list_1_cont,
        }
        self._list_all_objects_exec_assert(
            list_1_id, list_1_cont, expected_act_value_1, expected_act_value_2
        )

    def _list_all_objects_exec_assert(
        self,
        list_1_id,
        list_1_cont,
        expected_act_value_1,
        expected_act_value_2,
        **kwargs,
    ):
        entry_1 = BucketEntry(name="entry1")
        entry_2 = BucketEntry(name="entry2")
        entry_3 = BucketEntry(name="entry3")
        list_1 = BucketList(uuid=list_1_id, continuation_token=list_1_cont, flags=0)
        list_1.entries = [entry_1]
        list_2 = BucketList(uuid="456", continuation_token="", flags=0)
        list_2.entries = [entry_2, entry_3]

        self.mock_client.request_deserialize.return_value = BucketList(
            uuid="empty", continuation_token="", flags=0
        )
        self.assertEqual([], self.ais_bck.list_all_objects(**kwargs))

        self.mock_client.request_deserialize.side_effect = [list_1, list_2]
        self.assertEqual(
            [entry_1, entry_2, entry_3], self.ais_bck.list_all_objects(**kwargs)
        )

        call_1 = mock.call(
            HTTP_METHOD_GET,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            res_model=BucketList,
            json=ActionMsg(action=ACT_LIST, value=expected_act_value_1).dict(),
            params=self.ais_bck_params,
        )

        call_2 = mock.call(
            HTTP_METHOD_GET,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            res_model=BucketList,
            json=ActionMsg(action=ACT_LIST, value=expected_act_value_2).dict(),
            params=self.ais_bck_params,
        )

        self.mock_client.request_deserialize.assert_has_calls([call_1, call_2])

    def test_transform(self):
        etl_name = "etl-name"
        prefix = "prefix-"
        ext = {"jpg": "txt"}
        force = True
        dry_run = True
        action_value = {
            "id": etl_name,
            "prefix": prefix,
            "force": force,
            "dry_run": dry_run,
            "ext": ext,
        }

        self._transform_exec_assert(
            etl_name, action_value, prefix=prefix, ext=ext, force=force, dry_run=dry_run
        )

    def test_transform_default_params(self):
        etl_name = "etl-name"
        action_value = {"id": etl_name, "prefix": "", "force": False, "dry_run": False}

        self._transform_exec_assert(etl_name, action_value)

    def _transform_exec_assert(self, etl_name, expected_act_value, **kwargs):
        to_bck = "new-bucket"
        self.ais_bck_params[QParamBucketTo] = f"{ProviderAIS}/@#/{to_bck}/"
        expected_action = ActionMsg(action=ACT_ETL_BCK, value=expected_act_value).dict()
        expected_response = "job-id"
        mock_response = Mock()
        mock_response.text = expected_response
        self.mock_client.request.return_value = mock_response

        result_id = self.ais_bck.transform(etl_name, to_bck, **kwargs)

        self.mock_client.request.assert_called_with(
            HTTP_METHOD_POST,
            path=f"{URL_PATH_BUCKETS}/{BCK_NAME}",
            json=expected_action,
            params=self.ais_bck_params,
        )
        self.assertEqual(expected_response, result_id)

    def test_object(self):
        new_obj = self.ais_bck.object(obj_name="name")
        self.assertEqual(self.ais_bck, new_obj.bucket)

    @patch("aistore.sdk.object.read_file_bytes")
    @patch("aistore.sdk.object.validate_file")
    @patch("aistore.sdk.bucket.validate_directory")
    @patch("pathlib.Path.glob")
    def test_put_files(
        self, mock_glob, mock_validate_dir, mock_validate_file, mock_read
    ):
        path = "directory"
        file_1_name = "file_1_name"
        file_2_name = "file_2_name"
        path_1 = Mock()
        path_1.is_file.return_value = True
        path_1.relative_to.return_value = file_1_name
        path_2 = Mock()
        path_2.relative_to.return_value = file_2_name
        path_2.is_file.return_value = True
        file_1_data = b"bytes in the first file"
        file_2_data = b"bytes in the second file"
        mock_glob.return_value = [path_1, path_2]
        expected_obj_names = [file_1_name, file_2_name]
        mock_read.side_effect = [file_1_data, file_2_data]

        res = self.ais_bck.put_files(path)

        mock_validate_dir.assert_called_with(path)
        mock_validate_file.assert_has_calls([call(str(path_1)), call(str(path_2))])
        self.assertEqual(expected_obj_names, res)
        expected_calls = [
            call(
                HTTP_METHOD_PUT,
                path=f"objects/{BCK_NAME}/{file_1_name}",
                params=self.ais_bck_params,
                data=file_1_data,
            ),
            call(
                HTTP_METHOD_PUT,
                path=f"objects/{BCK_NAME}/{file_2_name}",
                params=self.ais_bck_params,
                data=file_2_data,
            ),
        ]
        self.mock_client.request.assert_has_calls(expected_calls)


if __name__ == "__main__":
    unittest.main()